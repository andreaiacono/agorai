package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repo is a discovered git repository offered in the "New session" picker.
type Repo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`    // absolute path used to spawn
	Display string `json:"display"` // home-shortened path for the UI
	Branch  string `json:"branch"`
	Sub     string `json:"sub"` // short commit + relative time
}

// discoverRepos walks each root (up to maxDepth levels) collecting directories
// that contain a .git entry. It does not descend into a repo once found.
func discoverRepos(roots []string, maxDepth int) []Repo {
	var out []Repo
	for _, root := range roots {
		walkRepos(root, 0, maxDepth, &out)
	}
	return out
}

func walkRepos(dir string, depth, maxDepth int, out *[]Repo) {
	if depth > maxDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			*out = append(*out, repoInfo(dir))
			return
		}
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			walkRepos(filepath.Join(dir, e.Name()), depth+1, maxDepth, out)
		}
	}
}

func repoInfo(dir string) Repo {
	return Repo{
		Name:    filepath.Base(dir),
		Path:    dir,
		Display: shortenHome(dir),
		Branch:  gitOut(dir, "rev-parse", "--abbrev-ref", "HEAD"),
		Sub:     gitOut(dir, "log", "-1", "--format=%h · %cr"),
	}
}

func gitOut(dir string, args ...string) string {
	c := exec.Command("git", args...)
	c.Dir = dir
	b, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func shortenHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// underRoots reports whether path is inside one of the allowed roots. This is a
// guardrail so a stray request can't spawn a shell anywhere on disk.
func underRoots(path string, roots []string) bool {
	clean := filepath.Clean(path)
	for _, root := range roots {
		if clean == root || strings.HasPrefix(clean, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}
