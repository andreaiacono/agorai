// Preferences for the agorai extension: currently just the notification toggle.
// The indicator (panel icon + badge + menu) is always on — this only gates the
// desktop notifications raised on permission prompts and finished turns.

import Adw from 'gi://Adw';
import Gio from 'gi://Gio';
import {ExtensionPreferences} from 'resource:///org/gnome/Shell/Extensions/js/extensions/prefs.js';

export default class AgoraiPreferences extends ExtensionPreferences {
    fillPreferencesWindow(window) {
        const settings = this.getSettings();

        const page = new Adw.PreferencesPage();
        const group = new Adw.PreferencesGroup({
            title: 'Notifications',
            description: 'The top-bar indicator keeps working either way.',
        });

        const row = new Adw.SwitchRow({
            title: 'Show desktop notifications',
            subtitle: 'When a session asks for permission or finishes a turn',
        });
        group.add(row);
        page.add(group);
        window.add(page);

        settings.bind('show-notifications', row, 'active', Gio.SettingsBindFlags.DEFAULT);
    }
}
