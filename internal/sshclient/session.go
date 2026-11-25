package sshclient

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// PTY describes the pseudo terminal to request.
type PTY struct {
	Term              string
	Cols, Rows        int
	WidthPx, HeightPx int
}

type ptyRequestMsg struct {
	Term     string
	Columns  uint32
	Rows     uint32
	Width    uint32
	Height   uint32
	Modelist string
}

type windowChangeMsg struct {
	Columns uint32
	Rows    uint32
	Width   uint32
	Height  uint32
}

// RequestPTY asks for a pseudo terminal with an explicit pixel size. The
// library's own RequestPty always claims 8x8 pixel cells, while
// terminal-browser reads the pixel size from the pty to size its frames.
func RequestPTY(s *ssh.Session, p PTY) error {
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 38400,
		ssh.TTY_OP_OSPEED: 38400,
		ssh.VERASE:        127,
		ssh.VINTR:         3,
		ssh.IUTF8:         1,
	}
	var tm []byte
	for k, v := range modes {
		tm = append(tm, k, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
	tm = append(tm, 0)
	req := ptyRequestMsg{
		Term:     p.Term,
		Columns:  uint32(p.Cols),
		Rows:     uint32(p.Rows),
		Width:    uint32(p.WidthPx),
		Height:   uint32(p.HeightPx),
		Modelist: string(tm),
	}
	ok, err := s.SendRequest("pty-req", true, ssh.Marshal(&req))
	if err == nil && !ok {
		err = errors.New("PTY allocation request failed")
	}
	return err
}

// WindowChange reports a new terminal size, with pixels.
func WindowChange(s *ssh.Session, cols, rows, widthPx, heightPx int) error {
	req := windowChangeMsg{uint32(cols), uint32(rows), uint32(widthPx), uint32(heightPx)}
	_, err := s.SendRequest("window-change", false, ssh.Marshal(&req))
	return err
}

// SetupEnv sends the environment variables SendEnv and SetEnv ask for,
// plus extra ones. Servers drop variables they do not accept; that is not
// an error.
func SetupEnv(s *ssh.Session, cfg *Config, extra map[string]string) {
	for _, v := range cfg.All("setenv") {
		for _, kv := range strings.Fields(v) {
			if k, val, ok := strings.Cut(kv, "="); ok {
				s.Setenv(k, strings.Trim(val, `"`))
			}
		}
	}
	var patterns []string
	for _, v := range cfg.All("sendenv") {
		patterns = append(patterns, strings.Fields(v)...)
	}
	if len(patterns) > 0 {
		for _, kv := range os.Environ() {
			k, val, _ := strings.Cut(kv, "=")
			for _, p := range patterns {
				if ok, _ := filepath.Match(p, k); ok && !strings.HasPrefix(p, "-") {
					s.Setenv(k, val)
					break
				}
			}
		}
	}
	for k, v := range extra {
		s.Setenv(k, v)
	}
}

// ForwardAgent enables agent forwarding for the session.
func ForwardAgent(conn *Connection, s *ssh.Session) error {
	if conn.Agent == nil {
		return errors.New("no agent to forward")
	}
	if err := agent.ForwardToAgent(conn.Client, conn.Agent.Client); err != nil {
		return err
	}
	return agent.RequestAgentForwarding(s)
}
