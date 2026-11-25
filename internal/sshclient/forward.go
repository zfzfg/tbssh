package sshclient

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// splitHostPort splits "[host]:port", "host:port" or "port".
func splitHostPort(s, defHost string) (string, string) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, ":") {
		return defHost, s
	}
	if strings.HasPrefix(s, "[") {
		if end := strings.Index(s, "]"); end >= 0 {
			host := s[1:end]
			rest := strings.TrimPrefix(s[end+1:], ":")
			return host, rest
		}
	}
	i := strings.LastIndexByte(s, ':')
	host := s[:i]
	if host == "" || host == "*" {
		host = "0.0.0.0"
	}
	return host, s[i+1:]
}

func listenAddr(spec string, gateway bool) string {
	def := "localhost"
	if gateway {
		def = "0.0.0.0"
	}
	h, p := splitHostPort(spec, def)
	return net.JoinHostPort(h, p)
}

// StartForwards sets up LocalForward, RemoteForward and DynamicForward from
// the configuration. Failures are reported as warnings, like ssh does.
func StartForwards(conn *Connection, warn func(string, ...any)) []io.Closer {
	cfg := conn.Config
	gateway := cfg.Bool("gatewayports", false)
	var closers []io.Closer
	for _, v := range cfg.All("localforward") {
		f := strings.Fields(v)
		if len(f) != 2 {
			continue
		}
		listen := listenAddr(f[0], gateway)
		th, tp := splitHostPort(f[1], "localhost")
		target := net.JoinHostPort(th, tp)
		l, err := net.Listen("tcp", listen)
		if err != nil {
			warn("Warning: Could not request local forwarding (%s): %v", listen, err)
			continue
		}
		closers = append(closers, l)
		go acceptLoop(l, func() (net.Conn, error) { return conn.Client.Dial("tcp", target) })
	}
	for _, v := range cfg.All("remoteforward") {
		f := strings.Fields(v)
		if len(f) != 2 {
			continue
		}
		rh, rp := splitHostPort(f[0], "localhost")
		th, tp := splitHostPort(f[1], "localhost")
		target := net.JoinHostPort(th, tp)
		l, err := conn.Client.Listen("tcp", net.JoinHostPort(rh, rp))
		if err != nil {
			warn("Warning: remote port forwarding failed for listen port %s", rp)
			continue
		}
		closers = append(closers, l)
		go acceptLoop(l, func() (net.Conn, error) { return net.Dial("tcp", target) })
	}
	for _, v := range cfg.All("dynamicforward") {
		listen := listenAddr(strings.TrimSpace(v), gateway)
		l, err := net.Listen("tcp", listen)
		if err != nil {
			warn("Warning: Could not request dynamic forwarding (%s): %v", listen, err)
			continue
		}
		closers = append(closers, l)
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go socks5(c, conn.Client)
			}
		}()
	}
	return closers
}

func acceptLoop(l net.Listener, dial func() (net.Conn, error)) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			t, err := dial()
			if err != nil {
				c.Close()
				return
			}
			pipe(c, t)
		}()
	}
}

func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		wg.Done()
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}

// socks5 serves one SOCKS5 (and SOCKS4a) CONNECT request through the SSH
// connection.
func socks5(c net.Conn, client *ssh.Client) {
	defer func() {
		if r := recover(); r != nil {
			c.Close()
		}
	}()
	target, err := socksHandshake(c)
	if err != nil {
		c.Close()
		return
	}
	t, err := client.Dial("tcp", target.addr)
	if err != nil {
		target.fail(c)
		c.Close()
		return
	}
	target.ok(c)
	pipe(c, t)
}

type socksTarget struct {
	addr string
	v4   bool
}

func (s socksTarget) ok(c net.Conn) {
	if s.v4 {
		c.Write([]byte{0, 0x5a, 0, 0, 0, 0, 0, 0})
		return
	}
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
}

func (s socksTarget) fail(c net.Conn) {
	if s.v4 {
		c.Write([]byte{0, 0x5b, 0, 0, 0, 0, 0, 0})
		return
	}
	c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
}

func socksHandshake(c net.Conn) (socksTarget, error) {
	var head [2]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return socksTarget{}, err
	}
	switch head[0] {
	case 4:
		if head[1] != 1 {
			return socksTarget{}, errors.New("socks4: only CONNECT")
		}
		var rest [6]byte
		if _, err := io.ReadFull(c, rest[:]); err != nil {
			return socksTarget{}, err
		}
		port := binary.BigEndian.Uint16(rest[:2])
		ip := net.IP(rest[2:6])
		if _, err := readCString(c); err != nil { // user id
			return socksTarget{}, err
		}
		host := ip.String()
		if ip[0] == 0 && ip[1] == 0 && ip[2] == 0 && ip[3] != 0 {
			name, err := readCString(c)
			if err != nil {
				return socksTarget{}, err
			}
			host = name
		}
		return socksTarget{addr: net.JoinHostPort(host, strconv.Itoa(int(port))), v4: true}, nil
	case 5:
		methods := make([]byte, head[1])
		if _, err := io.ReadFull(c, methods); err != nil {
			return socksTarget{}, err
		}
		c.Write([]byte{5, 0})
		var req [4]byte
		if _, err := io.ReadFull(c, req[:]); err != nil {
			return socksTarget{}, err
		}
		if req[1] != 1 {
			c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
			return socksTarget{}, errors.New("socks5: only CONNECT")
		}
		var host string
		switch req[3] {
		case 1:
			b := make([]byte, 4)
			if _, err := io.ReadFull(c, b); err != nil {
				return socksTarget{}, err
			}
			host = net.IP(b).String()
		case 4:
			b := make([]byte, 16)
			if _, err := io.ReadFull(c, b); err != nil {
				return socksTarget{}, err
			}
			host = net.IP(b).String()
		case 3:
			var n [1]byte
			if _, err := io.ReadFull(c, n[:]); err != nil {
				return socksTarget{}, err
			}
			b := make([]byte, n[0])
			if _, err := io.ReadFull(c, b); err != nil {
				return socksTarget{}, err
			}
			host = string(b)
		default:
			return socksTarget{}, fmt.Errorf("socks5: bad address type %d", req[3])
		}
		var p [2]byte
		if _, err := io.ReadFull(c, p[:]); err != nil {
			return socksTarget{}, err
		}
		return socksTarget{addr: net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p[:]))))}, nil
	}
	return socksTarget{}, errors.New("not a SOCKS request")
}

func readCString(r io.Reader) (string, error) {
	var out []byte
	var b [1]byte
	for len(out) < 256 {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", err
		}
		if b[0] == 0 {
			return string(out), nil
		}
		out = append(out, b[0])
	}
	return "", errors.New("string too long")
}
