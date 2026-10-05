package panel

import (
	"bufio"
	"claude-squad/log"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The Enter hook asks the panel over a Unix socket whether it took the Enter:
// the request is "enter <pane>\n", the reply "handled\n" or "pass\n".
const (
	replyHandled = "handled"
	replyPass    = "pass"
)

// SocketPath is where the panel listens for the Enter hook.
func SocketPath(configDir string) string {
	return filepath.Join(configDir, "panel.sock")
}

// ServeHook answers Enter hook requests with handle until the returned stop
// is called. A socket file left by an earlier panel is replaced.
func ServeHook(path string, handle func(pane string) bool) (stop func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveHookConn(conn, handle)
		}
	}()
	return func() {
		listener.Close()
		os.Remove(path)
	}, nil
}

func serveHookConn(conn net.Conn, handle func(pane string) bool) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(hookTimeout))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	reply := replyPass
	if pane, ok := strings.CutPrefix(strings.TrimSpace(line), "enter "); ok && handle(pane) {
		reply = replyHandled
	}
	if _, err := fmt.Fprintln(conn, reply); err != nil {
		log.WarningLog.Printf("reply to Enter hook: %v", err)
	}
}

// hookTimeout bounds a hook request; tmux holds the user's next keys until the
// hook returns, so a stuck panel must not hold them for long.
const hookTimeout = 3 * time.Second

// AskHook asks the panel listening at path whether it takes the Enter typed
// on pane. Any failure, such as no panel running, reports false.
func AskHook(path, pane string) bool {
	conn, err := net.DialTimeout("unix", path, hookTimeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(hookTimeout))
	if _, err := fmt.Fprintf(conn, "enter %s\n", pane); err != nil {
		return false
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	return err == nil && strings.TrimSpace(reply) == replyHandled
}
