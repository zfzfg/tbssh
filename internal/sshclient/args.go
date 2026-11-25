// Package sshclient is a small SSH client that behaves like OpenSSH's ssh
// for interactive sessions: it lets OpenSSH resolve the configuration
// (ssh -G), then connects, authenticates and verifies host keys itself so
// the terminal stream, the pty size and window changes are under tbssh's
// control.
package sshclient

import (
	"fmt"
	"strings"
)

// OpenSSH's getopt string (ssh.c). Letters followed by ':' take an argument.
const optString = "1246ab:c:e:fgi:kl:m:no:p:qstvxAB:CD:E:F:GI:J:KL:MNO:P:Q:R:S:TVw:W:XYy"

// Args is the parsed ssh command line.
type Args struct {
	Raw         []string // the original arguments, handed to ssh -G
	Destination string
	Command     []string

	TTY     int  // number of -t flags
	NoTTY   bool // -T
	Verbose int
	Quiet   bool
	Escape  string // -e, "" when not given
	Forward int    // +1 for -A, -1 for -a, 0 unset
	IPv4    bool
	IPv6    bool
	Flags   map[byte]bool
	// Unsupported names the first option that tbssh does not handle itself;
	// such command lines go to the real ssh.exe.
	Unsupported string
}

// Options that make ssh do something other than an interactive session, or
// that tbssh does not implement. The real ssh.exe handles these.
var delegated = map[byte]string{
	'f': "-f (background)",
	'G': "-G (print config)",
	'N': "-N (no command)",
	'n': "-n (stdin from /dev/null)",
	'O': "-O (control command)",
	'M': "-M (master mode)",
	'S': "-S (control socket)",
	's': "-s (subsystem)",
	'V': "-V (version)",
	'Q': "-Q (query)",
	'W': "-W (stdio forwarding)",
	'w': "-w (tunnel device)",
	'X': "-X (X11 forwarding)",
	'Y': "-Y (trusted X11 forwarding)",
	'I': "-I (PKCS#11)",
	'K': "-K (GSSAPI delegation)",
	'B': "-B (bind interface)",
	'b': "-b (bind address)",
}

func takesArg(c byte) (known, arg bool) {
	i := strings.IndexByte(optString, c)
	if i < 0 || c == ':' {
		return false, false
	}
	return true, i+1 < len(optString) && optString[i+1] == ':'
}

// ParseArgs parses an ssh command line the way OpenSSH does: options may
// come before and after the destination, and the first non-option word
// after the destination starts the remote command.
func ParseArgs(argv []string) (*Args, error) {
	a := &Args{Raw: argv, Flags: map[byte]bool{}}
	i := 0
	for i < len(argv) {
		arg := argv[i]
		if arg == "--" {
			i++
			if a.Destination == "" && i < len(argv) {
				a.Destination = argv[i]
				i++
			}
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			if a.Destination != "" {
				break // start of the remote command
			}
			a.Destination = arg
			i++
			continue
		}
		// A cluster of flags like -tt or -vp22.
		j := 1
		for j < len(arg) {
			c := arg[j]
			known, hasArg := takesArg(c)
			if !known {
				return nil, fmt.Errorf("unknown option -- %c", c)
			}
			var val string
			if hasArg {
				if j+1 < len(arg) {
					val = arg[j+1:]
				} else {
					i++
					if i >= len(argv) {
						return nil, fmt.Errorf("option requires an argument -- %c", c)
					}
					val = argv[i]
				}
				j = len(arg)
			} else {
				j++
			}
			a.apply(c, val)
		}
		i++
	}
	if a.Destination == "" {
		return nil, fmt.Errorf("no destination given")
	}
	a.Command = argv[i:]
	return a, nil
}

func (a *Args) apply(c byte, val string) {
	a.Flags[c] = true
	if reason, ok := delegated[c]; ok && a.Unsupported == "" {
		a.Unsupported = reason
	}
	switch c {
	case 't':
		a.TTY++
	case 'T':
		a.NoTTY = true
	case 'v':
		a.Verbose++
	case 'q':
		a.Quiet = true
	case 'e':
		a.Escape = val
	case 'A':
		a.Forward = 1
	case 'a':
		a.Forward = -1
	case '4':
		a.IPv4 = true
	case '6':
		a.IPv6 = true
	}
}
