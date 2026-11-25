package sshclient

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the effective configuration for one host, as printed by
// "ssh -G". Keys are lower case; repeated keys keep all values in order.
type Config struct {
	values map[string][]string
	SSH    string // the ssh executable used to resolve it
}

// Get returns the first value of key, or "".
func (c *Config) Get(key string) string {
	if v := c.values[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// All returns every value of key.
func (c *Config) All(key string) []string { return c.values[key] }

// Bool reads a yes/no option.
func (c *Config) Bool(key string, def bool) bool {
	switch strings.ToLower(c.Get(key)) {
	case "yes", "true", "on":
		return true
	case "no", "false", "off":
		return false
	}
	return def
}

// Int reads a numeric option.
func (c *Config) Int(key string, def int) int {
	n, err := strconv.Atoi(c.Get(key))
	if err != nil {
		return def
	}
	return n
}

// Host returns the host name to connect to.
func (c *Config) Host() string { return c.Get("hostname") }

// User returns the remote user name.
func (c *Config) User() string { return c.Get("user") }

// Port returns the remote port.
func (c *Config) Port() int { return c.Int("port", 22) }

// FindSSH returns the OpenSSH client to use for resolving the configuration
// and for command lines tbssh hands over: $TBSSH_SSH, Windows' own OpenSSH,
// or the first ssh.exe on PATH that is not tbssh itself.
func FindSSH() (string, error) {
	if p := os.Getenv("TBSSH_SSH"); p != "" {
		return p, nil
	}
	if sys := os.Getenv("SystemRoot"); sys != "" {
		p := filepath.Join(sys, "System32", "OpenSSH", "ssh.exe")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	self, _ := os.Executable()
	p, err := exec.LookPath("ssh.exe")
	if err != nil {
		return "", fmt.Errorf("OpenSSH client (ssh.exe) not found; install the Windows feature \"OpenSSH Client\" or set TBSSH_SSH")
	}
	if abs, _ := filepath.Abs(p); strings.EqualFold(abs, self) {
		return "", fmt.Errorf("ssh.exe on PATH is tbssh itself; set TBSSH_SSH to the real OpenSSH client")
	}
	return p, nil
}

// Resolve asks OpenSSH for the effective configuration of the given
// command line (ssh -G). It honours ~/.ssh/config, Include, Match, -F, -o,
// -p, -l, -i, -J and every other option exactly like ssh itself.
func Resolve(sshPath string, args []string) (*Config, error) {
	cmd := exec.Command(sshPath, append([]string{"-G"}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return ParseConfig(stdout.Bytes(), sshPath), nil
}

// ParseConfig parses "ssh -G" output.
func ParseConfig(out []byte, sshPath string) *Config {
	c := &Config{values: map[string][]string{}, SSH: sshPath}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		key = strings.ToLower(key)
		c.values[key] = append(c.values[key], strings.TrimSpace(val))
	}
	return c
}

// ExpandPath expands ~, __PROGRAMDATA__ and the common % tokens in a path
// taken from the configuration.
func (c *Config) ExpandPath(p string) string {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(p, "~") {
		p = home + p[1:]
	}
	p = strings.ReplaceAll(p, "__PROGRAMDATA__", os.Getenv("ProgramData"))
	if strings.Contains(p, "%") {
		local := ""
		if u, err := user.Current(); err == nil {
			local = u.Username
			if i := strings.LastIndexByte(local, '\\'); i >= 0 {
				local = local[i+1:]
			}
		}
		r := strings.NewReplacer(
			"%%", "%",
			"%d", home,
			"%u", local,
			"%h", c.Host(),
			"%r", c.User(),
			"%p", strconv.Itoa(c.Port()),
			"%n", c.Get("host"),
		)
		p = r.Replace(p)
	}
	return filepath.FromSlash(p)
}

// ExpandCommand expands tokens in ProxyCommand-style strings.
func (c *Config) ExpandCommand(s string) string {
	r := strings.NewReplacer(
		"%%", "%",
		"%h", c.Host(),
		"%p", strconv.Itoa(c.Port()),
		"%r", c.User(),
		"%n", c.Get("host"),
	)
	return r.Replace(s)
}

// Files returns the space separated paths of a key like userknownhostsfile,
// expanded.
func (c *Config) Files(key string) []string {
	var out []string
	for _, v := range c.All(key) {
		for _, f := range strings.Fields(v) {
			if strings.EqualFold(f, "none") {
				continue
			}
			out = append(out, c.ExpandPath(f))
		}
	}
	return out
}
