package sshclient

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeys verifies server host keys against known_hosts files, the way
// OpenSSH does with StrictHostKeyChecking.
type HostKeys struct {
	cfg       *Config
	userFiles []string
	db        ssh.HostKeyCallback
	prompt    Prompter
	hostname  string // name used for known_hosts lookups (alias or host)
	port      int
}

// NewHostKeys loads the known_hosts files named by the configuration.
func NewHostKeys(cfg *Config, prompt Prompter) (*HostKeys, error) {
	h := &HostKeys{cfg: cfg, prompt: prompt, port: cfg.Port()}
	h.hostname = cfg.Host()
	if alias := cfg.Get("hostkeyalias"); alias != "" && !strings.EqualFold(alias, "none") {
		h.hostname = alias
	}
	h.userFiles = cfg.Files("userknownhostsfile")
	files := append([]string{}, h.userFiles...)
	files = append(files, cfg.Files("globalknownhostsfile")...)
	var existing []string
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && !st.IsDir() {
			existing = append(existing, f)
		}
	}
	if len(existing) > 0 {
		db, err := knownhosts.New(existing...)
		if err != nil {
			return nil, err
		}
		h.db = db
	}
	return h, nil
}

func (h *HostKeys) address() string {
	return net.JoinHostPort(h.hostname, strconv.Itoa(h.port))
}

// KnownTypes returns the key types known_hosts holds for this host.
func (h *HostKeys) KnownTypes() []string {
	if h.db == nil {
		return nil
	}
	pub, _, _ := ed25519.GenerateKey(nil)
	probe, _ := ssh.NewPublicKey(pub)
	err := h.db(h.address(), dummyAddr(h.port), probe)
	var ke *knownhosts.KeyError
	if !errors.As(err, &ke) {
		return nil
	}
	seen := map[string]bool{}
	var types []string
	for _, k := range ke.Want {
		t := k.Key.Type()
		if !seen[t] {
			seen[t] = true
			types = append(types, t)
		}
	}
	return types
}

// HostKeyAlgorithms orders the client's host key algorithms so key types
// already in known_hosts are negotiated first, like OpenSSH does. It
// returns nil when nothing is known, which keeps the library defaults.
func (h *HostKeys) HostKeyAlgorithms() []string {
	known := h.KnownTypes()
	if len(known) == 0 {
		return nil
	}
	all := []string{
		ssh.CertAlgoED25519v01, ssh.CertAlgoECDSA256v01, ssh.CertAlgoECDSA384v01, ssh.CertAlgoECDSA521v01,
		ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01,
		ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA,
	}
	var first, rest []string
	for _, alg := range all {
		if matchesKnown(alg, known) {
			first = append(first, alg)
		} else {
			rest = append(rest, alg)
		}
	}
	return append(first, rest...)
}

func matchesKnown(alg string, known []string) bool {
	for _, t := range known {
		switch {
		case t == alg:
			return true
		case t == ssh.KeyAlgoRSA && (alg == ssh.KeyAlgoRSASHA256 || alg == ssh.KeyAlgoRSASHA512):
			return true
		}
	}
	return false
}

type dummyAddr int

func (d dummyAddr) Network() string { return "tcp" }
func (d dummyAddr) String() string  { return net.JoinHostPort("0.0.0.0", strconv.Itoa(int(d))) }

// Callback returns the ssh.HostKeyCallback to use for the connection.
func (h *HostKeys) Callback() ssh.HostKeyCallback {
	return func(_ string, remote net.Addr, key ssh.PublicKey) error {
		var err error
		if h.db != nil {
			err = h.db(h.address(), dummyAddr(h.port), key)
			if err == nil {
				return nil
			}
		} else {
			err = &knownhosts.KeyError{}
		}
		var revoked *knownhosts.RevokedError
		if errors.As(err, &revoked) {
			return fmt.Errorf("host key for %s is marked as revoked", h.hostname)
		}
		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) {
			return err
		}
		for _, w := range ke.Want {
			if w.Key.Type() == key.Type() {
				return h.changed(key, w)
			}
		}
		return h.unknown(remote, key)
	}
}

func (h *HostKeys) changed(key ssh.PublicKey, old knownhosts.KnownKey) error {
	msg := fmt.Sprintf(`@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
IT IS POSSIBLE THAT SOMEONE IS DOING SOMETHING NASTY!
Someone could be eavesdropping on you right now (man-in-the-middle attack)!
It is also possible that a host key has just been changed.
The fingerprint for the %s key sent by the remote host is
%s.
Offending key in %s:%d
Host key for %s has changed and you have requested strict checking.
Host key verification failed.`,
		keyName(key.Type()), ssh.FingerprintSHA256(key), old.Filename, old.Line, h.hostname)
	return errors.New(msg)
}

func (h *HostKeys) unknown(remote net.Addr, key ssh.PublicKey) error {
	mode := strings.ToLower(h.cfg.Get("stricthostkeychecking"))
	fp := ssh.FingerprintSHA256(key)
	switch mode {
	case "yes", "true":
		return fmt.Errorf("No %s host key is known for %s and you have requested strict checking.\nHost key verification failed.", keyName(key.Type()), h.hostname)
	case "no", "off", "false", "accept-new":
		if err := h.add(key); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to add the host to the list of known hosts (%v).\r\n", err)
		} else if mode != "accept-new" {
			fmt.Fprintf(os.Stderr, "Warning: Permanently added '%s' (%s) to the list of known hosts.\r\n", h.display(), keyName(key.Type()))
		}
		return nil
	}
	if h.cfg.Bool("batchmode", false) || h.prompt == nil {
		return errors.New("Host key verification failed.")
	}
	ip := ""
	if ta, ok := remote.(*net.TCPAddr); ok && ta.IP != nil && !ta.IP.IsUnspecified() && ta.IP.String() != h.hostname {
		ip = " (" + ta.IP.String() + ")"
	}
	fmt.Fprintf(os.Stderr, "The authenticity of host '%s%s' can't be established.\r\n%s key fingerprint is %s.\r\n", h.display(), ip, keyName(key.Type()), fp)
	fmt.Fprintf(os.Stderr, "This key is not known by any other names.\r\n")
	question := "Are you sure you want to continue connecting (yes/no/[fingerprint])? "
	for {
		answer, err := h.prompt.Line(question)
		if err != nil {
			return errors.New("Host key verification failed.")
		}
		answer = strings.TrimSpace(answer)
		switch {
		case strings.EqualFold(answer, "yes"), answer == fp:
			if err := h.add(key); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to add the host to the list of known hosts (%v).\r\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "Warning: Permanently added '%s' (%s) to the list of known hosts.\r\n", h.display(), keyName(key.Type()))
			}
			return nil
		case strings.EqualFold(answer, "no"):
			return errors.New("Host key verification failed.")
		}
		question = "Please type 'yes', 'no' or the fingerprint: "
	}
}

func (h *HostKeys) display() string {
	if h.port == 22 {
		return h.hostname
	}
	return fmt.Sprintf("[%s]:%d", h.hostname, h.port)
}

func (h *HostKeys) add(key ssh.PublicKey) error {
	if len(h.userFiles) == 0 {
		return errors.New("no UserKnownHostsFile")
	}
	file := h.userFiles[0]
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	name := knownhosts.Normalize(h.address())
	if h.cfg.Bool("hashknownhosts", false) {
		name = knownhosts.HashHostname(name)
	}
	line := knownhosts.Line([]string{name}, key)
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// Make sure the new entry starts on its own line.
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		if r, err := os.Open(file); err == nil {
			last := make([]byte, 1)
			if _, err := r.ReadAt(last, st.Size()-1); err == nil && last[0] != '\n' {
				line = "\n" + line
			}
			r.Close()
		}
	}
	_, err = f.WriteString(line + "\n")
	return err
}

func keyName(t string) string {
	switch {
	case strings.Contains(t, "ed25519"):
		return "ED25519"
	case strings.Contains(t, "ecdsa"):
		return "ECDSA"
	case strings.Contains(t, "rsa"):
		return "RSA"
	}
	return strings.ToUpper(t)
}
