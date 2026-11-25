package sshclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Connection is an authenticated SSH connection, possibly through jump
// hosts.
type Connection struct {
	Client *ssh.Client
	Config *Config
	Agent  *Agent
	hops   []*ssh.Client
}

// Close closes the connection and all jump hosts.
func (c *Connection) Close() {
	if c.Client != nil {
		c.Client.Close()
	}
	for i := len(c.hops) - 1; i >= 0; i-- {
		c.hops[i].Close()
	}
	c.Agent.Close()
}

// DialOptions controls Connect.
type DialOptions struct {
	Prompt  Prompter
	Logf    func(string, ...any)
	Verbose int
	IPv4    bool
	IPv6    bool
}

// Connect opens an authenticated connection for cfg. Jump hosts named by
// ProxyJump are resolved with "ssh -G" too, so their own configuration
// applies.
func Connect(cfg *Config, opt DialOptions) (*Connection, error) {
	conn := &Connection{Config: cfg}
	conn.Agent = OpenAgent(cfg)
	if conn.Agent != nil && opt.Logf != nil {
		opt.Logf("using agent at %s", conn.Agent.Path)
	}

	var via *ssh.Client
	if jump := cfg.Get("proxyjump"); jump != "" && !strings.EqualFold(jump, "none") {
		for _, hop := range strings.Split(jump, ",") {
			hop = strings.TrimSpace(hop)
			hopCfg, err := Resolve(cfg.SSH, []string{hop})
			if err != nil {
				conn.Close()
				return nil, fmt.Errorf("jump host %s: %v", hop, err)
			}
			client, err := dialOne(hopCfg, via, conn.Agent, opt)
			if err != nil {
				conn.Close()
				return nil, fmt.Errorf("jump host %s: %w", hop, err)
			}
			conn.hops = append(conn.hops, client)
			via = client
		}
	}
	client, err := dialOne(cfg, via, conn.Agent, opt)
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.Client = client
	return conn, nil
}

func dialOne(cfg *Config, via *ssh.Client, ag *Agent, opt DialOptions) (*ssh.Client, error) {
	host, port := cfg.Host(), cfg.Port()
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	timeout := time.Duration(0)
	if t := cfg.Int("connecttimeout", 0); t > 0 {
		timeout = time.Duration(t) * time.Second
	}

	var raw net.Conn
	var err error
	switch pc := cfg.Get("proxycommand"); {
	case via != nil:
		raw, err = via.Dial("tcp", addr)
	case pc != "" && !strings.EqualFold(pc, "none"):
		raw, err = proxyCommand(cfg.ExpandCommand(pc))
	default:
		network := "tcp"
		switch {
		case opt.IPv4 || strings.EqualFold(cfg.Get("addressfamily"), "inet"):
			network = "tcp4"
		case opt.IPv6 || strings.EqualFold(cfg.Get("addressfamily"), "inet6"):
			network = "tcp6"
		}
		d := net.Dialer{Timeout: timeout}
		raw, err = d.DialContext(context.Background(), network, addr)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to host %s port %d: %v", host, port, friendly(err))
	}

	hk, err := NewHostKeys(cfg, opt.Prompt)
	if err != nil {
		raw.Close()
		return nil, err
	}
	clientCfg := &ssh.ClientConfig{
		User:              cfg.User(),
		Auth:              AuthMethods(cfg, ag, opt.Prompt, opt.Logf),
		HostKeyCallback:   hk.Callback(),
		HostKeyAlgorithms: hk.HostKeyAlgorithms(),
		Timeout:           timeout,
		ClientVersion:     "SSH-2.0-tbssh_1.0",
	}
	if timeout > 0 {
		raw.SetDeadline(time.Now().Add(timeout + 30*time.Second))
	}
	c, chans, reqs, err := ssh.NewClientConn(raw, addr, clientCfg)
	if err != nil {
		raw.Close()
		return nil, authError(cfg, err)
	}
	raw.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

func friendly(err error) string {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		msg := op.Err.Error()
		if strings.Contains(msg, "refused") {
			return "Connection refused"
		}
		if strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out") {
			return "Connection timed out"
		}
		return msg
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "Could not resolve hostname: " + dns.Name
	}
	return err.Error()
}

func authError(cfg *Config, err error) error {
	msg := err.Error()
	if strings.Contains(msg, "unable to authenticate") {
		return fmt.Errorf("%s@%s: Permission denied (%s)", cfg.User(), cfg.Host(), extractMethods(msg))
	}
	if strings.Contains(msg, "Host key verification failed") || strings.Contains(msg, "REMOTE HOST IDENTIFICATION") ||
		strings.Contains(msg, "host key") {
		// ssh: handshake failed: <our message>
		if i := strings.Index(msg, "handshake failed: "); i >= 0 {
			msg = msg[i+len("handshake failed: "):]
		}
		return errors.New(msg)
	}
	return err
}

func extractMethods(msg string) string {
	i := strings.Index(msg, "attempted methods [")
	if i < 0 {
		return "publickey"
	}
	rest := msg[i+len("attempted methods ["):]
	if j := strings.IndexByte(rest, ']'); j >= 0 {
		rest = rest[:j]
	}
	parts := strings.Fields(rest)
	var out []string
	for _, p := range parts {
		if p != "none" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return "publickey"
	}
	return strings.Join(out, ",")
}

// proxyCommand runs a ProxyCommand and uses its stdin/stdout as the
// connection.
func proxyCommand(command string) (net.Conn, error) {
	cmd := exec.Command("cmd.exe", "/d", "/c", command)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &pipeConn{r: stdout, w: stdin, cmd: cmd}, nil
}

type pipeConn struct {
	r   io.ReadCloser
	w   io.WriteCloser
	cmd *exec.Cmd
}

func (p *pipeConn) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeConn) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeConn) Close() error {
	p.w.Close()
	p.r.Close()
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	return nil
}
func (p *pipeConn) LocalAddr() net.Addr                { return pipeAddr{} }
func (p *pipeConn) RemoteAddr() net.Addr               { return pipeAddr{} }
func (p *pipeConn) SetDeadline(t time.Time) error      { return nil }
func (p *pipeConn) SetReadDeadline(t time.Time) error  { return nil }
func (p *pipeConn) SetWriteDeadline(t time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "0.0.0.0:0" }
