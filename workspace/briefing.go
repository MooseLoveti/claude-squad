package workspace

import (
	"claude-squad/relay"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Briefing is what m's agent is told at launch about the other members of w.
func Briefing(w *Workspace, m Member, exe string) string {
	var peers []relay.Peer
	for _, o := range w.Members {
		if o.Agent == m.Agent || o.Program == "" {
			continue
		}
		peers = append(peers, relay.Peer{Agent: relay.AgentByName(o.Agent), Dir: o.Dir, Branch: o.Branch})
	}
	if len(peers) == 0 {
		return ""
	}
	return relay.Briefing(relay.AgentByName(m.Agent), m.Dir, peers, exe)
}

// briefingMarker starts every briefing, telling an instructions file written
// here from one the user wrote.
const briefingMarker = "[claude-squad]"

// WriteBriefings gives each agent that takes no instructions on its command
// line its briefing as the instructions file it reads from its own worktree,
// kept out of git through the repository's info/exclude. A shared directory is
// the user's own, and a file the repository tracks or the user wrote is
// theirs, so neither is touched; that agent then starts without a briefing.
func WriteBriefings(w *Workspace, exe string) error {
	for _, m := range w.Members {
		file := relay.AgentByName(m.Agent).BriefingFile()
		// In a shared directory every member runs in w.Dir.
		if file == "" || m.Program == "" || m.Dir == w.Dir {
			continue
		}
		if err := writeBriefing(m.Dir, file, Briefing(w, m, exe)); err != nil {
			return err
		}
	}
	return nil
}

func writeBriefing(dir, file, briefing string) error {
	if briefing == "" {
		return nil
	}
	path := filepath.Join(dir, file)
	existing, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case !strings.HasPrefix(string(existing), briefingMarker):
		return nil
	}
	if _, err := gitOutput(dir, "ls-files", "--error-unmatch", file); err == nil {
		return nil
	}
	if err := excludeFromGit(dir, "/"+file); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(briefing+"\n"), 0o644)
}

// excludeFromGit adds pattern to the repository's info/exclude unless it is
// already there.
func excludeFromGit(dir, pattern string) error {
	path, err := gitOutput(dir, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		pattern = "\n" + pattern
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(pattern + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
