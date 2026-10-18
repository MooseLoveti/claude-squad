// Package picker opens the desktop's own folder chooser. Under WSL that is
// the Windows dialog, reached through powershell.exe.
package picker

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf16"
)

// ErrCancelled means the dialog was closed without choosing a folder.
var ErrCancelled = errors.New("folder choice cancelled")

// ErrUnsupported means there is no folder dialog to open here.
var ErrUnsupported = errors.New("このPCではフォルダ選択ダイアログを開けません（WSL の powershell.exe が必要です）")

//go:embed pick_folder.ps1
var pickFolderScript string

// Folder asks for a folder with the Windows dialog, starting at initial
// (a WSL path), and returns the chosen folder as a WSL path.
func Folder(ctx context.Context, title, initial string) (string, error) {
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		return "", ErrUnsupported
	}
	start := ""
	if initial != "" {
		if win, err := wslpath(ctx, "-w", initial); err == nil {
			start = win
		}
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodeCommand(script(title, start)))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("フォルダ選択ダイアログ: %w %s", err, strings.TrimSpace(stderr.String()))
	}
	chosen := strings.TrimSpace(string(out))
	if chosen == "" {
		return "", ErrCancelled
	}
	return wslpath(ctx, "-u", chosen)
}

// script fills the dialog's title and starting folder into the PowerShell
// script, as single-quoted strings.
func script(title, start string) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	return "$title = " + quote(title) + "\n$initial = " + quote(start) + "\n" + pickFolderScript
}

// encodeCommand encodes a script for powershell -EncodedCommand, which takes
// base64 of UTF-16LE and so needs no quoting at all.
func encodeCommand(script string) string {
	units := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(units))
	for i, u := range units {
		b[2*i], b[2*i+1] = byte(u), byte(u>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func wslpath(ctx context.Context, flag, path string) (string, error) {
	out, err := exec.CommandContext(ctx, "wslpath", flag, path).Output()
	if err != nil {
		return "", fmt.Errorf("wslpath %s %s: %w", flag, path, err)
	}
	return strings.TrimSpace(string(out)), nil
}
