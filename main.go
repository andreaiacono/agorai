// agorai — a local dashboard for juggling multiple Claude Code sessions.
//
// It spawns each `claude` session in a PTY, bridges it to the browser over a
// WebSocket (rendered by xterm.js), and uses Claude Code hooks to know when a
// session is waiting for input. One self-contained binary; the web assets are
// embedded.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	// Subcommand: `agorai install` wires up the Claude Code hooks.
	if len(os.Args) > 1 && os.Args[1] == "install" {
		if err := installHooks(); err != nil {
			log.Fatalf("install: %v", err)
		}
		fmt.Println("✓ wrote ~/.claude/hooks/agorai.sh and merged ~/.claude/settings.json (backup: settings.json.agorai.bak)")
		fmt.Println("  restart agorai (and any running sessions) so they pick up the hook")
		return
	}

	addr := flag.String("addr", "127.0.0.1:7777", "address to listen on (keep it on localhost)")
	rootsFlag := flag.String("roots", defaultRoots(), "comma-separated dirs to scan for git repos")
	flag.Parse()

	roots := splitRoots(*rootsFlag)

	store := newStore()
	cfg := newConfigStore()
	mgr := NewManager(store, cfg)
	hub := newHub()
	srv := &Server{mgr: mgr, hub: hub, roots: roots, cfg: cfg}

	// Any change to a session (spawn, state change, exit) re-broadcasts the
	// session list to every connected control client.
	mgr.onChange = srv.broadcastSessions

	// Bring back the sessions that were open before the last shutdown.
	if n := mgr.RestoreAll(); n > 0 {
		log.Printf("restored %d session(s) from %s", n, store.path)
	}

	if !hooksInstalled() {
		log.Printf("WARNING: agorai hook not found in ~/.claude/settings.json — needs-input blink and live recap updates won't work. See README 'Wire up the hooks'. (Persistence works without it.)")
	}

	log.Printf("agorai listening on http://%s  (roots: %s)", *addr, strings.Join(roots, ", "))
	log.Printf("add the hook script to your Claude settings.json — see README.md")

	if err := http.ListenAndServe(*addr, srv.routes()); err != nil {
		log.Fatal(err)
	}
}

func hooksInstalled() bool {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	return err == nil && strings.Contains(string(b), "agorai")
}

func defaultRoots() string {
	home, _ := os.UserHomeDir()
	// Point directly at the repos to offer: a root that itself contains .git is
	// listed as that single repo (walkRepos stops there), so only these show.
	// Override with -roots to scan whole trees instead.
	return strings.Join([]string{
		filepath.Join(home, "dev", "light"),
		filepath.Join(home, "dev", "axolotl"),
		filepath.Join(home, "dev", "terraform"),
	}, ",")
}

func splitRoots(s string) []string {
	var out []string
	for _, r := range strings.Split(s, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, filepath.Clean(r))
		}
	}
	return out
}
