package picker

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/require"
)

func decodeCommand(t *testing.T, encoded string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(units))
}

func TestScriptQuotesTitleAndStart(t *testing.T) {
	got := script("作業フォルダ's", `C:\it's here`)
	require.True(t, strings.HasPrefix(got, "$title = '作業フォルダ''s'\n$initial = 'C:\\it''s here'\n"))
	require.Contains(t, got, "FOS_PICKFOLDERS")
	require.Equal(t, got, decodeCommand(t, encodeCommand(got)), "the encoding round-trips, wide characters included")
}

// fakeTools puts fake powershell.exe and wslpath first on PATH. powershell
// records its arguments and prints out; wslpath maps paths with a prefix.
func fakeTools(t *testing.T, out string, fail bool) string {
	bin := t.TempDir()
	ps := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + filepath.Join(bin, "ps.args") + "\n"
	if fail {
		ps += "echo boom >&2\nexit 1\n"
	} else {
		ps += "printf '%s' '" + out + "'\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(bin, "powershell.exe"), []byte(ps), 0o755))
	wsl := "#!/bin/sh\nif [ \"$1\" = -w ]; then printf 'WIN:%s\\n' \"$2\"; else printf '/mnt/%s\\n' \"$2\"; fi\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "wslpath"), []byte(wsl), 0o755))
	t.Setenv("PATH", bin)
	return bin
}

func TestFolderRunsTheWindowsDialog(t *testing.T) {
	bin := fakeTools(t, `C:\work\プロジェクト`, false)
	got, err := Folder(context.Background(), "選んで", "/home/me/src")
	require.NoError(t, err)
	require.Equal(t, `/mnt/C:\work\プロジェクト`, got, "the chosen path is converted back")

	args, err := os.ReadFile(filepath.Join(bin, "ps.args"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	require.Equal(t, []string{"-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-EncodedCommand"}, lines[:5])
	require.Contains(t, decodeCommand(t, lines[5]), "$initial = 'WIN:/home/me/src'", "the dialog starts at the current folder")
}

func TestFolderCancelledAndFailures(t *testing.T) {
	fakeTools(t, "", false)
	_, err := Folder(context.Background(), "t", "")
	require.ErrorIs(t, err, ErrCancelled)

	fakeTools(t, "", true)
	_, err = Folder(context.Background(), "t", "")
	require.ErrorContains(t, err, "boom")

	t.Setenv("PATH", t.TempDir())
	_, err = Folder(context.Background(), "t", "")
	require.ErrorIs(t, err, ErrUnsupported)
}
