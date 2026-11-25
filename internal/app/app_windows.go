//go:build windows

// Package app is tbssh's command line program.
package app

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"tbssh/internal/bridge"
	"tbssh/internal/console"
	"tbssh/internal/sshclient"
)

// Version is the tbssh version.
const Version = "0.1.0"

const usage = `tbssh %s - ssh for Windows Terminal that shows kitty graphics (terminal-browser) as sixel

Usage:
  tbssh [ssh options] destination [command]   like ssh; interactive sessions get the graphics bridge
  tbssh --doctor                              check the local terminal
  tbssh --demo                                draw an animated test image through the bridge
  tbssh --replay FILE                         play back a recording made with TBSSH_RECORD
  tbssh --version

Everything tbssh does not handle itself (no tty, -N, -W, -f, -X, ...) is passed
unchanged to the OpenSSH client (%s).

Environment:
  TBSSH_GRAPHICS=auto|on|off   kitty to sixel translation (auto: when the terminal draws sixel)
  TBSSH_FPS=60                 frame rate limit for unsynchronized updates
  TBSSH_SSH=path               OpenSSH client to use for config and pass-through
  TBSSH_DEBUG=1                write a log to %%LOCALAPPDATA%%\tbssh\tbssh.log
  TBSSH_RECORD=file            save the raw remote output for --replay
`

var logger = log.New(io.Discard, "", log.Ltime|log.Lmicroseconds)

func setupLog() {
	if os.Getenv("TBSSH_DEBUG") == "" {
		return
	}
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "tbssh")
	os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "tbssh.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	logger.SetOutput(f)
	logger.Printf("tbssh %s start, args %q", Version, os.Args[1:])
}

func logf(format string, args ...any) { logger.Printf(format, args...) }

// Main runs tbssh and returns the exit code.
func Main(argv []string) int {
	setupLog()
	if len(argv) == 1 {
		switch argv[0] {
		case "--version":
			fmt.Printf("tbssh %s\n", Version)
			return 0
		case "--help", "-h":
			p, _ := sshclient.FindSSH()
			fmt.Printf(usage, Version, p)
			return 0
		case "--doctor":
			return doctor()
		case "--demo":
			return demo()
		}
	}
	if len(argv) == 2 && argv[0] == "--replay" {
		return replay(argv[1])
	}

	sshPath, err := sshclient.FindSSH()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tbssh: %v\n", err)
		return 255
	}
	if len(argv) == 0 {
		return delegate(sshPath, argv, "no arguments")
	}
	args, err := sshclient.ParseArgs(argv)
	if err != nil {
		return delegate(sshPath, argv, err.Error())
	}
	if args.Unsupported != "" {
		return delegate(sshPath, argv, args.Unsupported)
	}
	if !console.IsConsole() {
		return delegate(sshPath, argv, "not a console")
	}
	cfg, err := sshclient.Resolve(sshPath, argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 255
	}
	if reason := needsOpenSSH(cfg, args); reason != "" {
		return delegate(sshPath, argv, reason)
	}
	return interactive(cfg, args)
}

// needsOpenSSH returns why a resolved session should be left to ssh.exe.
func needsOpenSSH(cfg *sshclient.Config, args *sshclient.Args) string {
	if t := strings.ToLower(cfg.Get("sessiontype")); t != "" && t != "default" {
		return "SessionType " + t
	}
	if cfg.Bool("stdinnull", false) {
		return "StdinNull"
	}
	if cfg.Bool("forkafterauthentication", false) {
		return "ForkAfterAuthentication"
	}
	if cfg.Bool("permitlocalcommand", false) && cfg.Get("localcommand") != "" {
		return "LocalCommand"
	}
	if strings.EqualFold(cfg.Get("controlmaster"), "yes") || strings.EqualFold(cfg.Get("controlmaster"), "auto") {
		return "ControlMaster"
	}
	if !wantsTTY(cfg, args) {
		return "no tty requested"
	}
	return ""
}

func wantsTTY(cfg *sshclient.Config, args *sshclient.Args) bool {
	if args.NoTTY {
		return false
	}
	if args.TTY > 0 {
		return true
	}
	switch strings.ToLower(cfg.Get("requesttty")) {
	case "no", "false":
		return false
	case "yes", "true", "force":
		return true
	}
	return remoteCommand(cfg, args) == ""
}

func remoteCommand(cfg *sshclient.Config, args *sshclient.Args) string {
	if len(args.Command) > 0 {
		return strings.Join(args.Command, " ")
	}
	if rc := cfg.Get("remotecommand"); rc != "" && !strings.EqualFold(rc, "none") {
		return rc
	}
	return ""
}

// delegate runs the real OpenSSH client with the same arguments.
func delegate(sshPath string, argv []string, reason string) int {
	logf("delegating to %s: %s", sshPath, reason)
	cmd := exec.Command(sshPath, argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	go func() {
		for range sig {
		}
	}()
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "tbssh: %v\n", err)
		return 255
	}
	return 0
}

func graphicsMode() string {
	switch strings.ToLower(os.Getenv("TBSSH_GRAPHICS")) {
	case "on", "1", "yes", "force":
		return "on"
	case "off", "0", "no":
		return "off"
	}
	return "auto"
}

func maxFPS() int {
	if n, err := strconv.Atoi(os.Getenv("TBSSH_FPS")); err == nil && n > 0 {
		return n
	}
	return 60
}

func interactive(cfg *sshclient.Config, args *sshclient.Args) int {
	quiet := args.Quiet || strings.EqualFold(cfg.Get("loglevel"), "quiet")
	verbose := func(format string, a ...any) {
		logf(format, a...)
		if args.Verbose > 0 {
			fmt.Fprintf(os.Stderr, "debug1: "+format+"\r\n", a...)
		}
	}
	verbose("tbssh %s connecting to %s port %d as %s", Version, cfg.Host(), cfg.Port(), cfg.User())

	conn, err := sshclient.Connect(cfg, sshclient.DialOptions{
		Prompt:  sshclient.ConsolePrompter{},
		Logf:    verbose,
		Verbose: args.Verbose,
		IPv4:    args.IPv4,
		IPv6:    args.IPv6,
	})
	if err != nil {
		msg := err.Error()
		if strings.HasPrefix(msg, "connect to host") || strings.HasPrefix(msg, "jump host") {
			msg = "ssh: " + msg
		}
		fmt.Fprintf(os.Stderr, "%s\r\n", msg)
		return 255
	}
	defer conn.Close()

	forwards := sshclient.StartForwards(conn, func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, format+"\r\n", a...)
	})
	defer func() {
		for _, c := range forwards {
			c.Close()
		}
	}()

	session, err := conn.Client.NewSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh: channel open failed: %v\r\n", err)
		return 255
	}
	defer session.Close()

	if args.Forward == 1 || (args.Forward == 0 && cfg.Bool("forwardagent", false)) {
		if err := sshclient.ForwardAgent(conn, session); err != nil {
			verbose("agent forwarding: %v", err)
		}
	}
	sshclient.SetupEnv(session, cfg, map[string]string{
		"LC_TERMINAL":         "tbssh",
		"LC_TERMINAL_VERSION": Version,
	})

	con, err := console.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tbssh: %v\r\n", err)
		return 255
	}
	if err := con.MakeRaw(); err != nil {
		fmt.Fprintf(os.Stderr, "tbssh: %v\r\n", err)
		return 255
	}
	defer con.Restore()

	graphics := false
	caps := console.Capabilities{}
	switch graphicsMode() {
	case "on":
		caps = con.Probe(time.Second)
		graphics = true
	case "auto":
		caps = con.Probe(time.Second)
		graphics = caps.Sixel
	}
	logf("terminal DA1=%q sixel=%v bg=%v graphics=%v", caps.DA1, caps.Sixel, caps.Background, graphics)

	cols, rows := con.Size()
	var stdinMu sync.Mutex
	stdin, err := session.StdinPipe()
	if err != nil {
		return 255
	}
	writeRemote := func(p []byte) {
		stdinMu.Lock()
		stdin.Write(p)
		stdinMu.Unlock()
	}
	out := newOutQueue(os.Stdout, 32<<20)
	br := bridge.New(out, writeRemote, bridge.Options{
		Cols: cols, Rows: rows,
		Graphics:   graphics,
		Background: caps.Background,
		MaxFPS:     maxFPS(),
		Log:        logf,
	})

	term := os.Getenv("TERM")
	if term == "" {
		term = "xterm-256color"
	}
	pty := sshclient.PTY{Term: term, Cols: cols, Rows: rows}
	if graphics {
		pty.WidthPx, pty.HeightPx = br.PixelSize(cols, rows)
	}
	if err := sshclient.RequestPTY(session, pty); err != nil {
		con.Restore()
		fmt.Fprintf(os.Stderr, "%v\r\n", err)
		return 255
	}

	stdout, _ := session.StdoutPipe()
	stderr, _ := session.StderrPipe()

	if cmd := remoteCommand(cfg, args); cmd != "" {
		err = session.Start(cmd)
	} else {
		err = session.Shell()
	}
	if err != nil {
		con.Restore()
		fmt.Fprintf(os.Stderr, "ssh: %v\r\n", err)
		return 255
	}

	var record io.Writer
	if path := os.Getenv("TBSSH_RECORD"); path != "" {
		if f, err := os.Create(path); err == nil {
			defer f.Close()
			record = f
		}
	}

	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		buf := make([]byte, 256<<10)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if record != nil {
					record.Write(buf[:n])
				}
				br.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 16<<10)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				br.WriteRaw(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	stop := make(chan struct{})
	defer close(stop)
	go con.WatchSize(stop, func(c, r int) {
		br.Resize(c, r)
		w, h := 0, 0
		if graphics {
			w, h = br.PixelSize(c, r)
		}
		sshclient.WindowChange(session, c, r, w, h)
	})

	esc := newEscaper(cfg.Get("escapechar"))
	if args.Escape != "" {
		esc = newEscaper(args.Escape)
	}
	disconnected := make(chan struct{})
	var disconnectOnce sync.Once
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := con.Read(buf)
			if err != nil {
				return
			}
			out, action := esc.filter(buf[:n])
			if len(out) > 0 {
				writeRemote(out)
			}
			switch action {
			case escHelp:
				br.WriteRaw([]byte("~?\r\n" + escapeHelp))
			case escDisconnect:
				disconnectOnce.Do(func() { close(disconnected) })
				conn.Client.Close()
				return
			}
		}
	}()

	if n := cfg.Int("serveraliveinterval", 0); n > 0 {
		go keepAlive(conn.Client, time.Duration(n)*time.Second, cfg.Int("serveralivecountmax", 3))
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		for range sig {
			writeRemote([]byte{3})
		}
	}()

	if logger.Writer() != io.Discard {
		go func() {
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					logf("stats %+v", br.Stats)
				}
			}
		}()
	}

	waitErr := session.Wait()
	select {
	case <-outDone:
	case <-time.After(500 * time.Millisecond):
	}
	br.Close()
	out.Close()
	con.Restore()
	logf("session ended: %v, stats %+v", waitErr, br.Stats)

	code := 0
	var ee *ssh.ExitError
	var missing *ssh.ExitMissingError
	switch {
	case waitErr == nil:
	case errors.As(waitErr, &ee):
		code = ee.ExitStatus()
	case errors.As(waitErr, &missing):
		code = 255
	default:
		code = 255
	}
	select {
	case <-disconnected:
		code = 255
	default:
	}
	if !quiet {
		fmt.Fprintf(os.Stderr, "Connection to %s closed.\r\n", cfg.Host())
	}
	return code
}

func keepAlive(c *ssh.Client, every time.Duration, maxMissed int) {
	missed := 0
	t := time.NewTicker(every)
	defer t.Stop()
	for range t.C {
		done := make(chan error, 1)
		go func() {
			_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				return
			}
			missed = 0
		case <-time.After(every):
			missed++
			if missed >= maxMissed {
				fmt.Fprintf(os.Stderr, "Timeout, server not responding.\r\n")
				c.Close()
				return
			}
		}
	}
}
