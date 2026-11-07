package config

import (
	"claude-squad/log"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	log.Initialize("[test] ")
	defer log.Close()
	os.Exit(m.Run())
}

func TestLoadConfigCreatesTheDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cs", ConfigFileName)
	cfg := LoadConfigFrom(path)
	require.NotEmpty(t, cfg.BranchPrefix)
	require.FileExists(t, path)
}

func TestLoadConfigReadsProfilesAndKeepsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigFileName)
	require.NoError(t, os.WriteFile(path, []byte(`{"profiles":[{"name":"codex","program":"codex --search"}],"auto_yes":true}`), 0o644))
	cfg := LoadConfigFrom(path)
	require.Equal(t, []Profile{{Name: "codex", Program: "codex --search"}}, cfg.Profiles)
	require.NotEmpty(t, cfg.BranchPrefix, "unset fields keep their defaults; unknown ones are ignored")

	require.NoError(t, os.WriteFile(path, []byte("{"), 0o644))
	require.Empty(t, LoadConfigFrom(path).Profiles, "a broken file falls back to the default")
}

func TestProjectsAndExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.Equal(t, filepath.Join(home, "src"), (&Config{}).Projects())
	require.Equal(t, filepath.Join(home, "code"), (&Config{ProjectsDir: "~/code"}).Projects())
	require.Equal(t, "/abs", (&Config{ProjectsDir: "/abs"}).Projects())
	require.Equal(t, home, ExpandHome("~"))
	require.Equal(t, "~user/x", ExpandHome("~user/x"))
}

func TestStateRoundTripAndRecent(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFileName)
	state := LoadStateFrom(path)
	require.Nil(t, state.Workspace)
	for _, dir := range []string{"/a", "/b", "/a"} {
		state.AddRecent(dir)
	}
	require.Equal(t, []string{"/a", "/b"}, state.Recent)
	for i := 0; i < recentLimit+3; i++ {
		state.AddRecent(filepath.Join("/d", string(rune('a'+i))))
	}
	require.Len(t, state.Recent, recentLimit)
	state.Workspace = []byte(`{"dir":"/repo"}`)
	require.NoError(t, state.Save())

	loaded := LoadStateFrom(path)
	require.JSONEq(t, `{"dir":"/repo"}`, string(loaded.Workspace))
	require.Equal(t, state.Recent, loaded.Recent)

	require.NoError(t, os.WriteFile(path, []byte(`{"help_screens_seen":6,"instances":[]}`), 0o644))
	require.Nil(t, LoadStateFrom(path).Workspace, "the old instance list is ignored")
	require.NoError(t, os.WriteFile(path, []byte("{"), 0o644))
	require.Nil(t, LoadStateFrom(path).Workspace)
	require.Error(t, (&State{}).Save(), "a state not loaded from a file has nowhere to go")
}
