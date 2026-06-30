// agorai GNOME Shell extension — a top-bar indicator + desktop notifications
// for agorai sessions. It polls the local agorai server's /api/sessions every
// couple of seconds (the same data the web UI renders), shows a panel badge of
// how many sessions want your attention, lists them in a menu you can click to
// jump to, and raises a native notification when a session asks for permission
// or finishes a turn.
//
// Polling (rather than the control WebSocket) is deliberate: it has no
// connection state to babysit, reconnects for free when the server comes and
// goes, and 2s latency is irrelevant for an away-from-keyboard nudge.

import GObject from 'gi://GObject';
import St from 'gi://St';
import Clutter from 'gi://Clutter';
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import Soup from 'gi://Soup';

import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as PanelMenu from 'resource:///org/gnome/shell/ui/panelMenu.js';
import * as PopupMenu from 'resource:///org/gnome/shell/ui/popupMenu.js';
import * as MessageTray from 'resource:///org/gnome/shell/ui/messageTray.js';
import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';

const BASE = 'http://127.0.0.1:7777';
const POLL_SECONDS = 2;

// State buckets mirror the web UI. perm/waiting want action now (red); a
// finished-but-unviewed turn is "unread" (green); working is amber; the rest
// are idle (neutral).
const COLOR = {
    attention: '#f0506e', // perm / waiting — needs you
    unread: '#32d296',    // finished, not yet looked at
    working: '#e3b341',   // mid-turn
    idle: '#7b8694',      // idle / ended
};

function stateColor(s, unread) {
    if (s === 'perm' || s === 'waiting') return COLOR.attention;
    if (unread) return COLOR.unread;
    if (s === 'working') return COLOR.working;
    return COLOR.idle;
}

// Compact context-fill + account-limit readout for a session row. Context is
// available for claude + codex; the 5h/weekly limits are codex-only (claude's
// account quota isn't readable from disk).
function usageText(s) {
    const parts = [];
    if (s.ctxMax > 0)
        parts.push(`ctx ${Math.round((100 * s.ctxTokens) / s.ctxMax)}%`);
    if (s.limits)
        parts.push(`5h ${s.limits.pct5h}% · wk ${s.limits.pctWeek}%`);
    return parts.join('  ');
}

const AgoraiIndicator = GObject.registerClass(
class AgoraiIndicator extends PanelMenu.Button {
    _init(ext) {
        super._init(0.0, 'agorai', false);
        this._ext = ext;

        // Panel: a terminal glyph + a small count badge.
        const box = new St.BoxLayout({style_class: 'panel-status-menu-box'});
        this._icon = new St.Icon({
            icon_name: 'utilities-terminal-symbolic',
            style_class: 'system-status-icon',
        });
        this._count = new St.Label({
            text: '',
            y_align: Clutter.ActorAlign.CENTER,
            style_class: 'agorai-count',
            visible: false,
        });
        box.add_child(this._icon);
        box.add_child(this._count);
        this.add_child(box);

        this._placeholder = new PopupMenu.PopupMenuItem('Connecting to agorai…');
        this._placeholder.setSensitive(false);
        this.menu.addMenuItem(this._placeholder);

        this.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        const open = new PopupMenu.PopupMenuItem('Open agorai');
        open.connect('activate', () => this._ext.openUrl(BASE));
        this.menu.addMenuItem(open);
    }

    // Rebuild the per-session rows (everything above the separator).
    render(sessions, unread) {
        // Drop old session rows, keeping the trailing separator + "Open agorai".
        const items = this.menu._getMenuItems();
        for (const it of items) {
            if (it instanceof PopupMenu.PopupSeparatorMenuItem) break;
            it.destroy();
        }

        let insertAt = 0;
        if (!sessions.length) {
            const empty = new PopupMenu.PopupMenuItem('No sessions');
            empty.setSensitive(false);
            this.menu.addMenuItem(empty, insertAt++);
        }
        for (const s of sessions) {
            const item = new PopupMenu.PopupBaseMenuItem();
            const dot = new St.Label({
                text: '●',
                y_align: Clutter.ActorAlign.CENTER,
                style: `color: ${stateColor(s.state, unread.has(s.id))}; margin-right: 8px;`,
            });
            const name = new St.Label({text: s.name || '(unnamed)', x_expand: true});
            item.add_child(dot);
            item.add_child(name);
            const recap = (s.state === 'working') ? 'working…' : (s.recap || '');
            if (recap) {
                item.add_child(new St.Label({
                    text: recap.length > 28 ? recap.slice(0, 27) + '…' : recap,
                    style_class: 'agorai-recap',
                    y_align: Clutter.ActorAlign.CENTER,
                }));
            }
            const usage = usageText(s);
            if (usage) {
                item.add_child(new St.Label({
                    text: usage,
                    style_class: 'agorai-usage',
                    y_align: Clutter.ActorAlign.CENTER,
                }));
            }
            item.connect('activate', () => {
                unread.delete(s.id); // viewing it = read
                this._ext.openUrl(`${BASE}/#session=${s.id}`);
            });
            this.menu.addMenuItem(item, insertAt++);
        }
    }

    // Panel badge: count of sessions wanting attention, tinted by worst state.
    setBadge(count, worst) {
        if (count > 0) {
            this._count.text = String(count);
            this._count.visible = true;
        } else {
            this._count.visible = false;
        }
        this._icon.style = worst ? `color: ${worst};` : '';
    }

    setDisconnected() {
        this.setBadge(0, COLOR.idle);
        const items = this.menu._getMenuItems();
        for (const it of items) {
            if (it instanceof PopupMenu.PopupSeparatorMenuItem) break;
            it.destroy();
        }
        const off = new PopupMenu.PopupMenuItem('agorai not running');
        off.setSensitive(false);
        this.menu.addMenuItem(off, 0);
    }
});

export default class AgoraiExtension extends Extension {
    enable() {
        this._http = new Soup.Session();
        this._prev = {};               // id -> last seen state (transition detection)
        this._unread = new Set();       // finished, not yet viewed
        this._source = null;            // lazily created notification source

        this._indicator = new AgoraiIndicator(this);
        Main.panel.addToStatusArea('agorai', this._indicator);

        this._poll();
        this._timer = GLib.timeout_add_seconds(GLib.PRIORITY_DEFAULT, POLL_SECONDS, () => {
            this._poll();
            return GLib.SOURCE_CONTINUE;
        });
    }

    disable() {
        if (this._timer) {
            GLib.source_remove(this._timer);
            this._timer = null;
        }
        this._indicator?.destroy();
        this._indicator = null;
        this._source?.destroy();
        this._source = null;
        this._http?.abort();
        this._http = null;
        this._prev = null;
        this._unread = null;
    }

    _poll() {
        const msg = Soup.Message.new('GET', `${BASE}/api/sessions`);
        this._http.send_and_read_async(msg, GLib.PRIORITY_DEFAULT, null, (sess, res) => {
            let sessions;
            try {
                const bytes = sess.send_and_read_finish(res);
                if (msg.get_status() !== Soup.Status.OK || !bytes)
                    throw new Error(`status ${msg.get_status()}`);
                sessions = JSON.parse(new TextDecoder().decode(bytes.get_data())) || [];
            } catch {
                this._indicator?.setDisconnected();
                return;
            }
            this._apply(sessions);
        });
    }

    _apply(sessions) {
        for (const s of sessions) {
            const prev = this._prev[s.id];

            if (s.state === 'working') {
                this._unread.delete(s.id); // active again, nothing pending
            } else if (prev === 'working' && s.state === 'idle') {
                this._unread.add(s.id);
                if (prev !== undefined)
                    this._notify(`${s.name || 'A session'} finished`, s.recap || 'Turn complete.', s.id, false);
            }

            // Entering an attention state (only on a real, observed transition —
            // prev === undefined is the first poll, just prime without alerting).
            if (prev !== undefined && prev !== s.state && (s.state === 'perm' || s.state === 'waiting')) {
                const what = s.state === 'perm' ? 'needs permission' : 'is waiting for input';
                this._notify(`${s.name || 'A session'} ${what}`, s.recap || '', s.id, true);
            }

            this._prev[s.id] = s.state;
        }

        // Forget sessions that vanished.
        const live = new Set(sessions.map((s) => s.id));
        for (const id of Object.keys(this._prev))
            if (!live.has(id)) { delete this._prev[id]; this._unread.delete(id); }

        const attention = sessions.filter((s) => s.state === 'perm' || s.state === 'waiting');
        const count = sessions.filter(
            (s) => s.state === 'perm' || s.state === 'waiting' || this._unread.has(s.id)).length;
        const worst = attention.length ? COLOR.attention
            : (this._unread.size ? COLOR.unread
                : (sessions.some((s) => s.state === 'working') ? COLOR.working : null));

        this._indicator.setBadge(count, worst);
        this._indicator.render(sessions, this._unread);
    }

    _ensureSource() {
        if (this._source) return this._source;
        this._source = new MessageTray.Source({
            title: 'agorai',
            iconName: 'utilities-terminal-symbolic',
        });
        this._source.connect('destroy', () => { this._source = null; });
        Main.messageTray.add(this._source);
        return this._source;
    }

    _notify(title, body, id, urgent) {
        const source = this._ensureSource();
        const n = new MessageTray.Notification({
            source,
            title,
            body,
            urgency: urgent ? MessageTray.Urgency.CRITICAL : MessageTray.Urgency.NORMAL,
        });
        n.addAction('Open', () => {
            this._unread.delete(id);
            this.openUrl(`${BASE}/#session=${id}`);
        });
        source.addNotification(n);
    }

    openUrl(url) {
        try {
            Gio.AppInfo.launch_default_for_uri(url, null);
        } catch (e) {
            logError(e, 'agorai: failed to open URL');
        }
    }
}
