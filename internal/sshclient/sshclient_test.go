package sshclient

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/zfzfg/tbssh/internal/bridge"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		argv        []string
		dest        string
		cmd         []string
		tty         int
		notty       bool
		unsupported bool
	}{
		{[]string{"host"}, "host", nil, 0, false, false},
		{[]string{"-p", "2222", "user@host"}, "user@host", nil, 0, false, false},
		{[]string{"host", "-p2222", "ls", "-l"}, "host", []string{"ls", "-l"}, 0, false, false},
		{[]string{"-tt", "host", "top"}, "host", []string{"top"}, 2, false, false},
		{[]string{"-T", "host"}, "host", nil, 0, true, false},
		{[]string{"-N", "-L", "8080:localhost:80", "host"}, "host", nil, 0, false, true},
		{[]string{"-vA", "-o", "ForwardAgent=yes", "host"}, "host", nil, 0, false, false},
		{[]string{"-J", "jump", "-i", "key", "host"}, "host", nil, 0, false, false},
		{[]string{"-X", "host"}, "host", nil, 0, false, true},
	}
	for _, c := range cases {
		a, err := ParseArgs(c.argv)
		if err != nil {
			t.Fatalf("%v: %v", c.argv, err)
		}
		if a.Destination != c.dest || strings.Join(a.Command, " ") != strings.Join(c.cmd, " ") ||
			a.TTY != c.tty || a.NoTTY != c.notty || (a.Unsupported != "") != c.unsupported {
			t.Fatalf("%v: got %+v", c.argv, a)
		}
	}
	if _, err := ParseArgs([]string{"-p"}); err == nil {
		t.Fatal("missing option argument must fail")
	}
	if _, err := ParseArgs([]string{"-Z", "host"}); err == nil {
		t.Fatal("unknown option must fail")
	}
}

func TestConfigExpand(t *testing.T) {
	cfg := ParseConfig([]byte("hostname example.com\nuser alice\nport 2222\nidentityfile ~/.ssh/id_%r\nuserknownhostsfile C:\\Users\\x/.ssh/known_hosts ~/.ssh/known_hosts2\nglobalknownhostsfile __PROGRAMDATA__\\ssh/ssh_known_hosts\n"), "ssh")
	home, _ := os.UserHomeDir()
	if got := cfg.Files("identityfile")[0]; got != filepath.FromSlash(home+"/.ssh/id_alice") {
		t.Fatalf("identityfile %q", got)
	}
	if n := len(cfg.Files("userknownhostsfile")); n != 2 {
		t.Fatalf("known hosts files: %d", n)
	}
	if got := cfg.Files("globalknownhostsfile")[0]; !strings.HasPrefix(got, os.Getenv("ProgramData")) {
		t.Fatalf("global known hosts %q", got)
	}
	if cfg.Port() != 2222 || cfg.User() != "alice" {
		t.Fatal("port/user")
	}
}

// testServer is a minimal sshd that behaves like a remote terminal-browser:
// it probes for kitty graphics, waits for the answers and draws a frame.
type testServer struct {
	addr    string
	hostKey ssh.Signer
	allowed ssh.PublicKey

	mu       sync.Mutex
	pty      ptyRequestMsg
	resizes  []windowChangeMsg
	env      map[string]string
	gotReply string
}

func newTestServer(t *testing.T, allowed ssh.PublicKey) *testServer {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	hk, _ := ssh.NewSignerFromKey(priv)
	s := &testServer{hostKey: hk, allowed: allowed, env: map[string]string{}}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	s.addr = l.Addr().String()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *testServer) serve(c net.Conn) {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), s.allowed.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(s.hostKey)
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, requests, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.session(ch, requests)
	}
}

func (s *testServer) session(ch ssh.Channel, requests <-chan *ssh.Request) {
	for req := range requests {
		switch req.Type {
		case "pty-req":
			var p ptyRequestMsg
			ssh.Unmarshal(req.Payload, &p)
			s.mu.Lock()
			s.pty = p
			s.mu.Unlock()
			req.Reply(true, nil)
		case "env":
			var kv struct{ K, V string }
			ssh.Unmarshal(req.Payload, &kv)
			s.mu.Lock()
			s.env[kv.K] = kv.V
			s.mu.Unlock()
			req.Reply(true, nil)
		case "window-change":
			var w windowChangeMsg
			ssh.Unmarshal(req.Payload, &w)
			s.mu.Lock()
			s.resizes = append(s.resizes, w)
			s.mu.Unlock()
		case "shell":
			req.Reply(true, nil)
			go s.fakeBrowser(ch)
		default:
			req.Reply(false, nil)
		}
	}
}

func (s *testServer) fakeBrowser(ch ssh.Channel) {
	defer ch.Close()
	io.WriteString(ch, "\x1b7\x1b_Gi=4207,a=q,t=d,f=24,s=1,v=1;AAAA\x1b\\\x1b[c\x1b[16t")
	var got []byte
	buf := make([]byte, 256)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !(bytes.Contains(got, []byte("Gi=4207;OK")) && bytes.Contains(got, []byte("\x1b[6;20;10t"))) {
		n, err := ch.Read(buf)
		if err != nil {
			break
		}
		got = append(got, buf[:n]...)
	}
	s.mu.Lock()
	s.gotReply = string(got)
	w, h := int(s.pty.Width), int(s.pty.Height)
	s.mu.Unlock()
	px := make([]byte, 4*w*h)
	for i := 0; i < len(px); i += 4 {
		px[i], px[i+1], px[i+2], px[i+3] = 0, 128, 255, 255
	}
	frame := "\x1b[?1049h\x1b[?2026h\x1b[H" + string(rawTransmit(fmt.Sprintf("a=T,f=32,s=%d,v=%d,t=d,i=1,p=1,C=1,q=2", w, h), px)) + "\x1b[?2026l\x1b[?1049l"
	io.WriteString(ch, frame)
	status := make([]byte, 4)
	binary.BigEndian.PutUint32(status, 7)
	ch.SendRequest("exit-status", false, status)
}

func rawTransmit(keys string, rgba []byte) []byte {
	enc := []byte(base64Std(rgba))
	var b bytes.Buffer
	for i := 0; i < len(enc); i += 4096 {
		end := min(i+4096, len(enc))
		more := 0
		if end < len(enc) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(&b, "\x1b_G%s,m=%d;%s\x1b\\", keys, more, enc[i:end])
		} else {
			fmt.Fprintf(&b, "\x1b_Gm=%d;%s\x1b\\", more, enc[i:end])
		}
	}
	return b.Bytes()
}

type nopPrompt struct{}

func (nopPrompt) Line(string) (string, error)   { return "", errors.New("no prompts in tests") }
func (nopPrompt) Secret(string) (string, error) { return "", errors.New("no prompts in tests") }

func writeKey(t *testing.T, dir string) (string, ssh.PublicKey) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(priv, "test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "id_test")
	os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
	sp, _ := ssh.NewPublicKey(pub)
	return path, sp
}

func testConfig(addr, key, knownHosts, strict string) *Config {
	host, port, _ := net.SplitHostPort(addr)
	out := fmt.Sprintf("hostname %s\nport %s\nuser tester\nidentityfile %s\nuserknownhostsfile %s\nglobalknownhostsfile none\nstricthostkeychecking %s\nidentityagent none\nbatchmode yes\n",
		host, port, key, knownHosts, strict)
	return ParseConfig([]byte(out), "ssh")
}

func TestEndToEndBridgeOverSSH(t *testing.T) {
	dir := t.TempDir()
	keyPath, pub := writeKey(t, dir)
	srv := newTestServer(t, pub)
	known := filepath.Join(dir, "known_hosts")

	cfg := testConfig(srv.addr, keyPath, known, "accept-new")
	conn, err := Connect(cfg, DialOptions{Prompt: nopPrompt{}})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()
	if data, _ := os.ReadFile(known); !bytes.Contains(data, []byte("ssh-ed25519")) {
		t.Fatalf("accept-new did not record the host key: %q", data)
	}

	session, err := conn.Client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	SetupEnv(session, cfg, map[string]string{"LC_TERMINAL": "tbssh"})
	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()
	var out bytes.Buffer
	var mu sync.Mutex
	br := bridge.New(lockedWriter{&out, &mu}, func(p []byte) { stdin.Write(p) }, bridge.Options{Cols: 12, Rows: 5, Graphics: true})
	w, h := br.PixelSize(12, 5)
	if err := RequestPTY(session, PTY{Term: "xterm-256color", Cols: 12, Rows: 5, WidthPx: w, HeightPx: h}); err != nil {
		t.Fatal(err)
	}
	if err := session.Shell(); err != nil {
		t.Fatal(err)
	}
	if err := WindowChange(session, 13, 5, 130, 100); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		io.Copy(br, stdout)
		close(done)
	}()
	err = session.Wait()
	<-done
	br.Close()
	var ee *ssh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() != 7 {
		t.Fatalf("exit status: %v", err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.pty.Columns != 12 || srv.pty.Width != 120 || srv.pty.Height != 100 {
		t.Fatalf("pty request %+v", srv.pty)
	}
	if len(srv.resizes) != 1 || srv.resizes[0].Width != 130 {
		t.Fatalf("window changes %+v", srv.resizes)
	}
	if srv.env["LC_TERMINAL"] != "tbssh" {
		t.Fatalf("env %v", srv.env)
	}
	if !strings.Contains(srv.gotReply, "\x1b_Gi=4207;OK\x1b\\") || !strings.Contains(srv.gotReply, "\x1b[6;20;10t") {
		t.Fatalf("remote got %q", srv.gotReply)
	}
	mu.Lock()
	o := out.String()
	mu.Unlock()
	if !strings.Contains(o, "\x1bP0;1;0q\"1;1;120;100") || strings.Contains(o, "\x1b_G") {
		t.Fatalf("terminal output lacks the sixel frame: %q", truncate(o))
	}
	if !strings.Contains(o, "\x1b[c") {
		t.Fatal("DA1 must still reach the terminal")
	}
}

func TestHostKeyChecking(t *testing.T) {
	dir := t.TempDir()
	keyPath, pub := writeKey(t, dir)
	srv := newTestServer(t, pub)
	known := filepath.Join(dir, "known_hosts")

	// Unknown host with strict checking fails.
	if _, err := Connect(testConfig(srv.addr, keyPath, known, "yes"), DialOptions{Prompt: nopPrompt{}}); err == nil {
		t.Fatal("strict checking must refuse an unknown host")
	}
	// accept-new records it, then strict checking passes.
	c, err := Connect(testConfig(srv.addr, keyPath, known, "accept-new"), DialOptions{Prompt: nopPrompt{}})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	c, err = Connect(testConfig(srv.addr, keyPath, known, "yes"), DialOptions{Prompt: nopPrompt{}})
	if err != nil {
		t.Fatalf("known host refused: %v", err)
	}
	c.Close()
	// A different key for the same host is reported as changed.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	srv.hostKey, _ = ssh.NewSignerFromKey(priv)
	_, err = Connect(testConfig(srv.addr, keyPath, known, "ask"), DialOptions{Prompt: nopPrompt{}})
	if err == nil || !strings.Contains(err.Error(), "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		t.Fatalf("changed key not detected: %v", err)
	}
}

func TestWrongKeyIsDenied(t *testing.T) {
	dir := t.TempDir()
	keyPath, _ := writeKey(t, dir)
	_, other := writeKey(t, t.TempDir())
	srv := newTestServer(t, other)
	_, err := Connect(testConfig(srv.addr, keyPath, filepath.Join(dir, "kh"), "no"), DialOptions{Prompt: nopPrompt{}})
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func truncate(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
