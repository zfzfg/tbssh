package sshclient

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Prompter asks the user for input.
type Prompter interface {
	// Line reads a line with echo.
	Line(prompt string) (string, error)
	// Secret reads a line without echo.
	Secret(prompt string) (string, error)
}

// Agent is a connection to an SSH agent.
type Agent struct {
	Client agent.ExtendedAgent
	conn   net.Conn
	Path   string
}

// Close closes the agent connection.
func (a *Agent) Close() {
	if a != nil && a.conn != nil {
		a.conn.Close()
	}
}

// OpenAgent connects to the agent the configuration names: IdentityAgent,
// $SSH_AUTH_SOCK, or Windows' OpenSSH agent pipe.
func OpenAgent(cfg *Config) *Agent {
	path := cfg.Get("identityagent")
	switch {
	case strings.EqualFold(path, "none"):
		return nil
	case path == "" || path == "SSH_AUTH_SOCK" || path == "$SSH_AUTH_SOCK":
		path = os.Getenv("SSH_AUTH_SOCK")
	default:
		path = cfg.ExpandPath(path)
	}
	if path == "" {
		path = `\\.\pipe\openssh-ssh-agent`
	}
	conn, err := dialAgent(path)
	if err != nil {
		return nil
	}
	return &Agent{Client: agent.NewClient(conn), conn: conn, Path: path}
}

// AuthMethods builds the authentication methods in the order the
// configuration asks for.
func AuthMethods(cfg *Config, ag *Agent, prompt Prompter, logf func(string, ...any)) []ssh.AuthMethod {
	batch := cfg.Bool("batchmode", false)
	if batch {
		prompt = nil
	}
	order := strings.Split(cfg.Get("preferredauthentications"), ",")
	if cfg.Get("preferredauthentications") == "" {
		order = []string{"publickey", "keyboard-interactive", "password"}
	}
	userHost := fmt.Sprintf("%s@%s", cfg.User(), cfg.Host())
	var methods []ssh.AuthMethod
	for _, m := range order {
		switch strings.TrimSpace(m) {
		case "publickey":
			if !cfg.Bool("pubkeyauthentication", true) {
				continue
			}
			methods = append(methods, ssh.PublicKeysCallback(publicKeys(cfg, ag, prompt, logf)))
		case "keyboard-interactive":
			if !cfg.Bool("kbdinteractiveauthentication", true) || prompt == nil {
				continue
			}
			methods = append(methods, ssh.RetryableAuthMethod(ssh.KeyboardInteractive(
				func(name, instruction string, questions []string, echos []bool) ([]string, error) {
					if name != "" {
						fmt.Fprintf(os.Stderr, "%s\r\n", name)
					}
					if instruction != "" {
						fmt.Fprintf(os.Stderr, "%s\r\n", instruction)
					}
					answers := make([]string, len(questions))
					for i, q := range questions {
						var err error
						if echos[i] {
							answers[i], err = prompt.Line(q)
						} else {
							answers[i], err = prompt.Secret(q)
						}
						if err != nil {
							return nil, err
						}
					}
					return answers, nil
				}), cfg.Int("numberofpasswordprompts", 3)))
		case "password":
			if !cfg.Bool("passwordauthentication", true) || prompt == nil {
				continue
			}
			methods = append(methods, ssh.RetryableAuthMethod(ssh.PasswordCallback(func() (string, error) {
				return prompt.Secret(userHost + "'s password: ")
			}), cfg.Int("numberofpasswordprompts", 3)))
		}
	}
	return methods
}

// publicKeys returns the signers to offer: agent keys first, then identity
// files that the agent does not already hold. Encrypted key files only ask
// for their passphrase when the server accepts the key.
func publicKeys(cfg *Config, ag *Agent, prompt Prompter, logf func(string, ...any)) func() ([]ssh.Signer, error) {
	return func() ([]ssh.Signer, error) {
		var files []*fileKey
		for _, path := range cfg.Files("identityfile") {
			if k := loadFileKey(path, prompt, logf); k != nil {
				files = append(files, k)
			}
		}
		for _, path := range cfg.Files("certificatefile") {
			attachCert(files, path)
		}
		identitiesOnly := cfg.Bool("identitiesonly", false)

		var signers []ssh.Signer
		offered := map[string]bool{}
		if ag != nil {
			agentSigners, err := ag.Client.Signers()
			if err == nil {
				for _, s := range agentSigners {
					blob := string(s.PublicKey().Marshal())
					if identitiesOnly && !fileHasKey(files, s.PublicKey()) {
						continue
					}
					offered[blob] = true
					signers = append(signers, s)
				}
			}
		}
		for _, f := range files {
			if offered[string(f.pub.Marshal())] {
				continue
			}
			offered[string(f.pub.Marshal())] = true
			signers = append(signers, f)
		}
		return signers, nil
	}
}

func fileHasKey(files []*fileKey, pub ssh.PublicKey) bool {
	for _, f := range files {
		if bytes.Equal(f.base.Marshal(), pub.Marshal()) || bytes.Equal(f.pub.Marshal(), pub.Marshal()) {
			return true
		}
		if cert, ok := pub.(*ssh.Certificate); ok && bytes.Equal(cert.Key.Marshal(), f.base.Marshal()) {
			return true
		}
	}
	return false
}

// fileKey is a private key file that is decrypted on first use.
type fileKey struct {
	path   string
	raw    []byte
	base   ssh.PublicKey // the plain public key
	pub    ssh.PublicKey // what is offered: base, or a certificate for it
	cert   *ssh.Certificate
	prompt Prompter

	mu     sync.Mutex
	signer ssh.Signer
	failed error
}

func loadFileKey(path string, prompt Prompter, logf func(string, ...any)) *fileKey {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	k := &fileKey{path: path, raw: raw, prompt: prompt}
	signer, err := ssh.ParsePrivateKey(raw)
	var missing *ssh.PassphraseMissingError
	switch {
	case err == nil:
		k.signer = signer
		k.base = signer.PublicKey()
	case errors.As(err, &missing):
		k.base = missing.PublicKey
		if k.base == nil {
			if pubRaw, err := os.ReadFile(path + ".pub"); err == nil {
				k.base, _, _, _, _ = ssh.ParseAuthorizedKey(pubRaw)
			}
		}
		if k.base == nil {
			// Old PEM encryption without a .pub file: the public key is
			// only known after decrypting.
			if prompt == nil {
				return nil
			}
			if err := k.unlock(); err != nil {
				return nil
			}
		}
	default:
		if logf != nil {
			logf("skipping identity %s: %v", path, err)
		}
		return nil
	}
	k.pub = k.base
	if certRaw, err := os.ReadFile(path + "-cert.pub"); err == nil {
		attachCertBytes(k, certRaw)
	}
	return k
}

func attachCert(files []*fileKey, path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, f := range files {
		attachCertBytes(f, raw)
	}
}

func attachCertBytes(k *fileKey, raw []byte) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		return
	}
	cert, ok := pub.(*ssh.Certificate)
	if !ok || !bytes.Equal(cert.Key.Marshal(), k.base.Marshal()) {
		return
	}
	k.cert = cert
	k.pub = cert
}

func (k *fileKey) unlock() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.signer != nil {
		return nil
	}
	if k.failed != nil {
		return k.failed
	}
	if k.prompt == nil {
		k.failed = errors.New("passphrase required")
		return k.failed
	}
	for try := 0; try < 3; try++ {
		pass, err := k.prompt.Secret(fmt.Sprintf("Enter passphrase for key '%s': ", k.path))
		if err != nil {
			k.failed = err
			return err
		}
		if pass == "" {
			k.failed = errors.New("no passphrase given")
			return k.failed
		}
		signer, err := ssh.ParsePrivateKeyWithPassphrase(k.raw, []byte(pass))
		if err == nil {
			k.signer = signer
			if k.base == nil {
				k.base = signer.PublicKey()
			}
			return nil
		}
	}
	k.failed = errors.New("bad passphrase")
	return k.failed
}

func (k *fileKey) inner() (ssh.Signer, error) {
	if err := k.unlock(); err != nil {
		return nil, err
	}
	if k.cert != nil {
		return ssh.NewCertSigner(k.cert, k.signer)
	}
	return k.signer, nil
}

// PublicKey implements ssh.Signer.
func (k *fileKey) PublicKey() ssh.PublicKey { return k.pub }

// Sign implements ssh.Signer.
func (k *fileKey) Sign(rand io.Reader, data []byte) (*ssh.Signature, error) {
	s, err := k.inner()
	if err != nil {
		return nil, err
	}
	return s.Sign(rand, data)
}

// SignWithAlgorithm implements ssh.AlgorithmSigner, needed for rsa-sha2.
func (k *fileKey) SignWithAlgorithm(rand io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	s, err := k.inner()
	if err != nil {
		return nil, err
	}
	if as, ok := s.(ssh.AlgorithmSigner); ok {
		return as.SignWithAlgorithm(rand, data, algorithm)
	}
	return s.Sign(rand, data)
}

// Algorithms implements ssh.MultiAlgorithmSigner. Like the library's own
// signers it lists the underlying signature algorithms, also for
// certificates.
func (k *fileKey) Algorithms() []string {
	t := k.base.Type()
	if t == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{t}
}
