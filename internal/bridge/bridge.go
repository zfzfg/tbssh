// Package bridge sits between a remote program's terminal output and the
// local terminal. It forwards every byte unchanged except for kitty graphics
// commands, which it composes and draws as sixel images, and a handful of
// queries it answers itself so the remote side sees a kitty-capable terminal
// with a cell size that matches Windows Terminal's sixel grid.
package bridge

import (
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/zfzfg/tbssh/internal/compositor"
	"github.com/zfzfg/tbssh/internal/kitty"
	"github.com/zfzfg/tbssh/internal/sixel"
	"github.com/zfzfg/tbssh/internal/vtstream"
)

// Options configures a Bridge.
type Options struct {
	Cols, Rows   int
	CellW, CellH int
	// Graphics enables the kitty to sixel translation. Without it the
	// bridge forwards everything untouched.
	Graphics bool
	// Background is the terminal's background color, used where an image
	// is translucent or was removed.
	Background [3]byte
	// MaxFPS limits how often unsynchronized updates are drawn.
	MaxFPS int
	// Log receives debug messages; nil discards them.
	Log func(format string, args ...any)
}

// Bridge processes remote output. It is safe for concurrent use.
type Bridge struct {
	mu    sync.Mutex
	out   io.Writer
	reply func([]byte)
	opt   Options

	parser vtstream.Parser
	screen *vtstream.Screen
	asm    kitty.Assembler
	comp   *compositor.Compositor
	six    *sixel.Screen

	buf          []byte
	inBandResize bool
	lastRender   time.Time
	timer        *time.Timer
	closed       bool

	Stats Stats
}

// Stats counts what the bridge did.
type Stats struct {
	BytesIn, BytesOut  int64
	KittyCommands      int64
	Frames, SixelBytes int64
}

// New creates a bridge writing to out. reply sends bytes back to the remote
// program, as if the terminal had answered.
func New(out io.Writer, reply func([]byte), opt Options) *Bridge {
	if opt.CellW <= 0 {
		opt.CellW = 10
	}
	if opt.CellH <= 0 {
		opt.CellH = 20
	}
	if opt.MaxFPS <= 0 {
		opt.MaxFPS = 60
	}
	b := &Bridge{out: out, reply: reply, opt: opt}
	b.screen = vtstream.NewScreen(opt.Cols, opt.Rows)
	b.comp = compositor.New(b.screen.Cols, b.screen.Rows, opt.CellW, opt.CellH)
	b.comp.SetBackground(opt.Background[0], opt.Background[1], opt.Background[2])
	b.six = sixel.NewScreen(b.comp.W, b.comp.H)
	return b
}

func (b *Bridge) logf(format string, args ...any) {
	if b.opt.Log != nil {
		b.opt.Log(format, args...)
	}
}

// Graphics reports whether translation is on.
func (b *Bridge) Graphics() bool { return b.opt.Graphics }

// PixelSize returns the screen size in the pixels the remote side sees.
func (b *Bridge) PixelSize(cols, rows int) (w, h int) {
	return cols * b.opt.CellW, rows * b.opt.CellH
}

// Write takes remote output.
func (b *Bridge) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Stats.BytesIn += int64(len(p))
	if !b.opt.Graphics {
		b.Stats.BytesOut += int64(len(p))
		_, err := b.out.Write(p)
		return len(p), err
	}
	b.parser.Feed(p, b.token)
	if b.comp.Dirty() {
		if b.screen.Synchronized {
			// The frame is still being written; draw it when it ends, or
			// after a while if the end never comes.
			b.schedule(100 * time.Millisecond)
		} else {
			b.renderPaced()
		}
	}
	return len(p), b.flush()
}

// WriteRaw sends bytes to the terminal without looking at them, for example
// the remote's stderr.
func (b *Bridge) WriteRaw(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.flush(); err != nil {
		return 0, err
	}
	b.Stats.BytesOut += int64(len(p))
	return b.out.Write(p)
}

func (b *Bridge) flush() error {
	if len(b.buf) == 0 {
		return nil
	}
	b.Stats.BytesOut += int64(len(b.buf))
	_, err := b.out.Write(b.buf)
	b.buf = b.buf[:0]
	return err
}

func (b *Bridge) token(t vtstream.Token) {
	switch t.Kind {
	case vtstream.Text, vtstream.Esc:
		b.buf = append(b.buf, t.Raw...)
		b.screen.Apply(&t)
		b.events()
	case vtstream.StringData:
		b.buf = append(b.buf, t.Raw...)
	case vtstream.CSI:
		if b.csi(&t) {
			return
		}
		b.buf = append(b.buf, t.Raw...)
		b.screen.Apply(&t)
		b.events()
	case vtstream.APC:
		if len(t.Payload) > 0 && t.Payload[0] == 'G' {
			b.Stats.KittyCommands++
			b.kittyCommand(t.Payload[1:])
			return
		}
		b.buf = append(b.buf, t.Raw...)
	}
}

// csi handles the control sequences the bridge answers or rewrites. It
// returns true when the sequence must not be forwarded as is.
func (b *Bridge) csi(t *vtstream.Token) bool {
	switch {
	case t.Private == 0 && t.Final == 't' && len(t.Inter) == 0 && t.NumParams() == 1:
		switch t.Param(0, 0) {
		case 14: // text area size in pixels
			b.send(fmt.Sprintf("\x1b[4;%d;%dt", b.comp.H, b.comp.W))
			return true
		case 16: // cell size in pixels
			b.send(fmt.Sprintf("\x1b[6;%d;%dt", b.opt.CellH, b.opt.CellW))
			return true
		}
	case t.Private == '?' && t.Final == 'p' && string(t.Inter) == "$":
		switch t.Param(0, 0) {
		case 1016: // SGR pixel mouse: not available, the remote falls back to cells
			b.send("\x1b[?1016;0$y")
			return true
		case 2048: // in-band resize: emulated by the bridge
			state := 2
			if b.inBandResize {
				state = 1
			}
			b.send(fmt.Sprintf("\x1b[?2048;%d$y", state))
			return true
		}
	case t.Private == '?' && (t.Final == 'h' || t.Final == 'l') && len(t.Inter) == 0:
		return b.privateModes(t)
	}
	return false
}

func (b *Bridge) privateModes(t *vtstream.Token) bool {
	set := t.Final == 'h'
	n := t.NumParams()
	has2048 := false
	for i := 0; i < n; i++ {
		if t.Param(i, 0) == 2048 {
			has2048 = true
		}
	}
	if !has2048 {
		if !set && b.screen.Synchronized {
			for i := 0; i < n; i++ {
				if t.Param(i, 0) == 2026 {
					// End of a synchronized frame: draw the graphics inside
					// the frame, before the terminal shows it.
					b.screen.Apply(t)
					b.render()
					b.buf = append(b.buf, t.Raw...)
					return true
				}
			}
		}
		return false
	}
	// Strip 2048 (the bridge emulates it) and forward the rest.
	var rest []byte
	for i := 0; i < n; i++ {
		v := t.Param(i, 0)
		if v == 2048 {
			continue
		}
		if len(rest) > 0 {
			rest = append(rest, ';')
		}
		rest = strconv.AppendInt(rest, int64(v), 10)
	}
	if len(rest) > 0 {
		seq := append([]byte("\x1b[?"), rest...)
		seq = append(seq, t.Final)
		var p vtstream.Parser
		p.Feed(seq, func(tk vtstream.Token) {
			b.buf = append(b.buf, tk.Raw...)
			b.screen.Apply(&tk)
		})
		b.events()
	}
	if set && !b.inBandResize {
		b.inBandResize = true
		b.sendSize()
	}
	if !set {
		b.inBandResize = false
	}
	return true
}

func (b *Bridge) sendSize() {
	b.send(fmt.Sprintf("\x1b[48;%d;%d;%d;%dt", b.screen.Rows, b.screen.Cols, b.comp.H, b.comp.W))
}

func (b *Bridge) send(s string) {
	if b.reply != nil {
		b.reply([]byte(s))
	}
}

// events reacts to what the last token did to the screen.
func (b *Bridge) events() {
	ev := b.screen.TakeEvents()
	if ev.Cleared {
		b.comp.ClearPlacements()
		b.six.Forget()
	}
	if ev.Scrolled != 0 {
		b.comp.Scroll(ev.Scrolled, ev.ScrollTop, ev.ScrollBottom)
	}
	if ev.TouchedTop >= 0 {
		// Windows Terminal drops sixel pixels in cells that receive text,
		// so those rows have to be painted again if an image covers them.
		ch := b.opt.CellH
		b.six.ForgetRows(ev.TouchedTop*ch, (ev.TouchedBottom+1)*ch)
		if b.comp.CoversRows(ev.TouchedTop, ev.TouchedBottom) {
			b.comp.DamageRows(ev.TouchedTop, ev.TouchedBottom)
		}
	}
}

func (b *Bridge) kittyCommand(payload []byte) {
	cmd, err := b.asm.Feed(payload)
	if cmd == nil && err == nil {
		return
	}
	if err != nil {
		b.replyKitty(cmd, err.Error())
		return
	}
	switch cmd.Action() {
	case 'q':
		if len(cmd.Data) > 0 || cmd.Char('t', 'd') != 'd' {
			if _, _, _, err := kitty.DecodePixels(cmd); err != nil {
				b.replyKitty(cmd, err.Error())
				return
			}
		}
		b.replyKitty(cmd, "OK")
	case 't', 'T':
		w, h, pix, err := kitty.DecodePixels(cmd)
		if err != nil {
			b.replyKitty(cmd, err.Error())
			return
		}
		id := uint32(cmd.Int('i', 0))
		number := cmd.Int('I', 0)
		if id == 0 {
			id = b.comp.AllocID()
			if number != 0 {
				cmd.Keys['i'] = strconv.FormatUint(uint64(id), 10)
			}
		}
		b.comp.Transmit(id, number, w, h, pix)
		if cmd.Action() == 'T' {
			if msg := b.place(cmd, id); msg != "OK" {
				b.replyKitty(cmd, msg)
				return
			}
		}
		b.replyKitty(cmd, "OK")
	case 'p':
		id, ok := b.resolveID(cmd)
		if !ok {
			b.replyKitty(cmd, "ENOENT:image not found")
			return
		}
		b.replyKitty(cmd, b.place(cmd, id))
	case 'f':
		b.frame(cmd)
	case 'd':
		b.comp.Delete(compositor.DeleteSpec{
			What:      cmd.Char('d', 'a'),
			ImageID:   uint32(cmd.Int('i', 0)),
			Placement: uint32(cmd.Int('p', 0)),
			Number:    cmd.Int('I', 0),
			X:         cmd.Int('x', 0),
			Y:         cmd.Int('y', 0),
			Z:         cmd.Int('z', 0),
			CursorCol: b.screen.X,
			CursorRow: b.screen.Y,
		})
	default:
		// Animation control and composition are accepted but not shown.
		b.replyKitty(cmd, "OK")
	}
}

func (b *Bridge) resolveID(cmd *kitty.Command) (uint32, bool) {
	if id := cmd.Int('i', 0); id != 0 {
		return uint32(id), b.comp.Image(uint32(id)) != nil
	}
	if n := cmd.Int('I', 0); n != 0 {
		id, ok := b.comp.ImageByNumber(n)
		if ok {
			cmd.Keys['i'] = strconv.FormatUint(uint64(id), 10)
		}
		return id, ok
	}
	return 0, false
}

func (b *Bridge) place(cmd *kitty.Command, id uint32) string {
	if cmd.Int('U', 0) == 1 {
		// Unicode placeholders are not drawn; accept them quietly.
		return "OK"
	}
	p := compositor.Placement{
		Image: id,
		ID:    uint32(cmd.Int('p', 0)),
		Col:   b.screen.X,
		Row:   b.screen.Y,
		OffX:  cmd.Int('X', 0),
		OffY:  cmd.Int('Y', 0),
		Src:   compositor.Rect{X: cmd.Int('x', 0), Y: cmd.Int('y', 0), W: cmd.Int('w', 0), H: cmd.Int('h', 0)},
		Cols:  cmd.Int('c', 0),
		Rows:  cmd.Int('r', 0),
		Z:     cmd.Int('z', 0),
	}
	if p.OffX >= b.opt.CellW {
		p.OffX = b.opt.CellW - 1
	}
	if p.OffY >= b.opt.CellH {
		p.OffY = b.opt.CellH - 1
	}
	if !b.comp.Place(p) {
		return "ENOENT:image not found"
	}
	if cmd.Int('C', 0) != 1 {
		// Like kitty, move the cursor to the cell after the image's last
		// column, on its last row.
		cols, rows := b.comp.CellSpan(b.comp.Last())
		var move []byte
		for i := 1; i < rows; i++ {
			move = append(move, '\n')
		}
		move = append(move, fmt.Sprintf("\x1b[%dC", cols)...)
		var parser vtstream.Parser
		parser.Feed(move, func(tk vtstream.Token) {
			b.buf = append(b.buf, tk.Raw...)
			b.screen.Apply(&tk)
		})
		b.events()
	}
	return "OK"
}

func (b *Bridge) frame(cmd *kitty.Command) {
	id, ok := b.resolveID(cmd)
	if !ok {
		b.replyKitty(cmd, "ENOENT:image not found")
		return
	}
	// Only edits of the root frame are shown; other frames are animation
	// data that is never played back here.
	if r := cmd.Int('r', 0); r != 1 {
		b.replyKitty(cmd, "OK")
		return
	}
	if len(cmd.Data) == 0 {
		b.replyKitty(cmd, "OK")
		return
	}
	w, h, pix, err := kitty.DecodePixels(cmd)
	if err != nil {
		b.replyKitty(cmd, err.Error())
		return
	}
	b.comp.EditFrame(id, cmd.Int('x', 0), cmd.Int('y', 0), w, h, pix, cmd.Int('X', 0) == 1)
	b.replyKitty(cmd, "OK")
}

func (b *Bridge) replyKitty(cmd *kitty.Command, msg string) {
	if cmd == nil {
		return
	}
	if msg != "OK" {
		b.logf("kitty %c: %s", cmd.Action(), msg)
	}
	if r := kitty.Reply(cmd, msg); r != nil && b.reply != nil {
		b.reply(r)
	}
}

// render draws all pending graphics changes into the output buffer.
func (b *Bridge) render() {
	rects := b.comp.Render()
	if len(rects) == 0 {
		return
	}
	sr := make([]sixel.Rect, len(rects))
	for i, r := range rects {
		sr[i] = sixel.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H}
	}
	seq := b.six.Update(b.comp.Canvas, b.comp.Covered, sr, b.opt.Background)
	b.lastRender = time.Now()
	if len(seq) == 0 {
		return
	}
	b.Stats.Frames++
	b.Stats.SixelBytes += int64(len(seq))
	b.buf = append(b.buf, seq...)
}

func (b *Bridge) renderPaced() {
	interval := time.Second / time.Duration(b.opt.MaxFPS)
	if wait := interval - time.Since(b.lastRender); wait > 0 {
		b.schedule(wait)
		return
	}
	b.render()
}

func (b *Bridge) schedule(d time.Duration) {
	if b.timer != nil || b.closed {
		return
	}
	b.timer = time.AfterFunc(d, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.timer = nil
		if b.closed || !b.comp.Dirty() {
			return
		}
		b.render()
		_ = b.flush()
	})
}

// Resize tells the bridge the local terminal changed size.
func (b *Bridge) Resize(cols, rows int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cols == b.screen.Cols && rows == b.screen.Rows {
		return
	}
	b.screen.Resize(cols, rows)
	b.comp.Resize(b.screen.Cols, b.screen.Rows)
	b.six.Resize(b.comp.W, b.comp.H)
	if b.opt.Graphics && b.inBandResize {
		b.sendSize()
	}
}

// Close stops pending work and, if the remote program left the terminal in
// its full-screen state (for example because the connection dropped),
// restores it.
func (b *Bridge) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	if b.opt.Graphics && b.screen.AltScreen {
		b.buf = append(b.buf, "\x1b[?2026l\x1b[0m\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?1004l\x1b[?2004l\x1b[?25h\x1b[?1049l"...)
	}
	_ = b.flush()
}
