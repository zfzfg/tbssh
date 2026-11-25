//go:build windows

// Package console puts the Windows console into the raw virtual-terminal
// mode a remote shell needs, reads keyboard input as VT bytes, reports size
// changes and asks the terminal (Windows Terminal) a few questions.
package console

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procReadConsoleInputW = kernel32.NewProc("ReadConsoleInputW")
)

const (
	keyEvent = 0x0001
)

type inputRecord struct {
	EventType uint16
	_         uint16
	Event     [16]byte
}

// Console is the process's console, in and out.
type Console struct {
	In, Out          windows.Handle
	inMode, outMode  uint32
	inCP, outCP      uint32
	raw              bool
	restoreOnce      sync.Once
	pendingSurrogate uint16
}

// Open returns the console when both stdin and stdout are console handles.
func Open() (*Console, error) {
	c := &Console{
		In:  windows.Handle(os.Stdin.Fd()),
		Out: windows.Handle(os.Stdout.Fd()),
	}
	if err := windows.GetConsoleMode(c.In, &c.inMode); err != nil {
		return nil, errors.New("stdin is not a console")
	}
	if err := windows.GetConsoleMode(c.Out, &c.outMode); err != nil {
		return nil, errors.New("stdout is not a console")
	}
	c.inCP, _ = windows.GetConsoleCP()
	c.outCP, _ = windows.GetConsoleOutputCP()
	return c, nil
}

// IsConsole reports whether stdin and stdout are both consoles.
func IsConsole() bool {
	var m uint32
	return windows.GetConsoleMode(windows.Handle(os.Stdin.Fd()), &m) == nil &&
		windows.GetConsoleMode(windows.Handle(os.Stdout.Fd()), &m) == nil
}

// MakeRaw switches to raw VT input and VT output.
func (c *Console) MakeRaw() error {
	in := c.inMode
	in &^= windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT |
		windows.ENABLE_MOUSE_INPUT | windows.ENABLE_QUICK_EDIT_MODE
	in |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT | windows.ENABLE_WINDOW_INPUT | windows.ENABLE_EXTENDED_FLAGS
	if err := windows.SetConsoleMode(c.In, in); err != nil {
		return err
	}
	out := c.outMode | windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING | windows.DISABLE_NEWLINE_AUTO_RETURN
	if err := windows.SetConsoleMode(c.Out, out); err != nil {
		windows.SetConsoleMode(c.In, c.inMode)
		return errors.New("the console does not support virtual terminal sequences")
	}
	windows.SetConsoleCP(65001)
	windows.SetConsoleOutputCP(65001)
	c.raw = true
	return nil
}

// Restore puts the console back the way it was.
func (c *Console) Restore() {
	c.restoreOnce.Do(func() {
		if !c.raw {
			return
		}
		windows.SetConsoleMode(c.In, c.inMode)
		windows.SetConsoleMode(c.Out, c.outMode)
		if c.inCP != 0 {
			windows.SetConsoleCP(c.inCP)
		}
		if c.outCP != 0 {
			windows.SetConsoleOutputCP(c.outCP)
		}
	})
}

// Size returns the visible window size in cells.
func (c *Console) Size() (cols, rows int) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(c.Out, &info); err != nil {
		return 80, 24
	}
	cols = int(info.Window.Right-info.Window.Left) + 1
	rows = int(info.Window.Bottom-info.Window.Top) + 1
	if cols < 1 || rows < 1 {
		return 80, 24
	}
	return cols, rows
}

// WatchSize calls fn with the new size whenever the window size changes,
// until stop is closed.
func (c *Console) WatchSize(stop <-chan struct{}, fn func(cols, rows int)) {
	lastC, lastR := c.Size()
	t := time.NewTicker(120 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			cols, rows := c.Size()
			if cols != lastC || rows != lastR {
				lastC, lastR = cols, rows
				fn(cols, rows)
			}
		}
	}
}

// Read reads keyboard input as UTF-8 VT bytes. It blocks until at least one
// character is available.
func (c *Console) Read(p []byte) (int, error) {
	if len(p) < 8 {
		return 0, errors.New("console read buffer too small")
	}
	buf := make([]uint16, len(p)/4)
	var n uint32
	if err := windows.ReadConsole(c.In, &buf[0], uint32(len(buf)), &n, nil); err != nil {
		return 0, err
	}
	return c.decode(buf[:n], p), nil
}

func (c *Console) decode(units []uint16, p []byte) int {
	out := p[:0]
	if c.pendingSurrogate != 0 {
		units = append([]uint16{c.pendingSurrogate}, units...)
		c.pendingSurrogate = 0
	}
	for i := 0; i < len(units); i++ {
		u := units[i]
		var r rune
		switch {
		case utf16.IsSurrogate(rune(u)) && u < 0xdc00:
			if i+1 == len(units) {
				c.pendingSurrogate = u
				continue
			}
			r = utf16.DecodeRune(rune(u), rune(units[i+1]))
			i++
		default:
			r = rune(u)
		}
		out = utf8.AppendRune(out, r)
	}
	return len(out)
}

// readRecords reads key characters for up to timeout, for probing.
func (c *Console) readRecords(timeout time.Duration, done func([]byte) bool) []byte {
	var collected []byte
	deadline := time.Now().Add(timeout)
	recs := make([]inputRecord, 64)
	for !done(collected) {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		ev, err := windows.WaitForSingleObject(c.In, uint32(left/time.Millisecond)+1)
		if err != nil || ev != windows.WAIT_OBJECT_0 {
			break
		}
		var n uint32
		r1, _, _ := procReadConsoleInputW.Call(uintptr(c.In), uintptr(unsafe.Pointer(&recs[0])), uintptr(len(recs)), uintptr(unsafe.Pointer(&n)))
		if r1 == 0 {
			break
		}
		var units []uint16
		for _, rec := range recs[:n] {
			if rec.EventType != keyEvent {
				continue
			}
			down := *(*int32)(unsafe.Pointer(&rec.Event[0]))
			ch := *(*uint16)(unsafe.Pointer(&rec.Event[10]))
			if down != 0 && ch != 0 {
				units = append(units, ch)
			}
		}
		if len(units) > 0 {
			tmp := make([]byte, len(units)*4+4)
			k := c.decode(units, tmp)
			collected = append(collected, tmp[:k]...)
		}
	}
	return collected
}

// Capabilities is what the terminal said about itself.
type Capabilities struct {
	Sixel      bool
	Background [3]byte
	HasBG      bool
	DA1        string
}

var (
	da1Re = regexp.MustCompile(`\x1b\[\?([0-9;]*)c`)
	bgRe  = regexp.MustCompile(`\x1b\]11;rgb:([0-9a-fA-F]+)/([0-9a-fA-F]+)/([0-9a-fA-F]+)`)
)

// Probe asks the terminal for its device attributes and background color.
// It must run in raw mode, before anything else reads input.
func (c *Console) Probe(timeout time.Duration) Capabilities {
	os.Stdout.Write([]byte("\x1b]11;?\x1b\\\x1b[c"))
	reply := c.readRecords(timeout, func(b []byte) bool { return da1Re.Match(b) })
	return parseCapabilities(reply)
}

func parseCapabilities(reply []byte) Capabilities {
	var caps Capabilities
	if m := da1Re.FindSubmatch(reply); m != nil {
		caps.DA1 = string(m[1])
		for _, attr := range bytes.Split(m[1], []byte(";")) {
			if string(attr) == "4" {
				caps.Sixel = true
			}
		}
	}
	if m := bgRe.FindSubmatch(reply); m != nil {
		for i := 0; i < 3; i++ {
			caps.Background[i] = scaleHex(string(m[i+1]))
		}
		caps.HasBG = true
	}
	return caps
}

func scaleHex(s string) byte {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil || len(s) == 0 {
		return 0
	}
	maxV := uint64(1)<<(4*len(s)) - 1
	return byte(v * 255 / maxV)
}

// Ask writes a query and returns the raw reply, for diagnostics.
func (c *Console) Ask(query string, timeout time.Duration, done func([]byte) bool) []byte {
	os.Stdout.Write([]byte(query))
	return c.readRecords(timeout, done)
}
