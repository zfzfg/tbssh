package bridge

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

type sink struct {
	out     bytes.Buffer
	replies bytes.Buffer
}

func newBridge(graphics bool) (*Bridge, *sink) {
	s := &sink{}
	b := New(&s.out, func(p []byte) { s.replies.Write(p) }, Options{
		Cols: 8, Rows: 4, Graphics: graphics, Background: [3]byte{0, 0, 0},
	})
	return b, s
}

// transmit builds a terminal-browser style inline transmission: zlib RGBA,
// base64, split into 4096 byte chunks.
func transmit(keys string, rgba []byte) string {
	var z bytes.Buffer
	w, _ := zlib.NewWriterLevel(&z, 1)
	w.Write(rgba)
	w.Close()
	enc := base64.StdEncoding.EncodeToString(z.Bytes())
	var b strings.Builder
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
	return b.String()
}

func solid(w, h int, r, g, bl byte) []byte {
	px := make([]byte, w*h*4)
	for i := 0; i < len(px); i += 4 {
		px[i], px[i+1], px[i+2], px[i+3] = r, g, bl, 255
	}
	return px
}

func TestPassthroughWithoutGraphics(t *testing.T) {
	b, s := newBridge(false)
	in := "hello\x1b_Gi=4207,a=q,t=d,f=24,s=1,v=1;AAAA\x1b\\\x1b[c\x1b[16t"
	b.Write([]byte(in))
	if s.out.String() != in || s.replies.Len() != 0 {
		t.Fatalf("graphics off must forward everything: out=%q replies=%q", s.out.String(), s.replies.String())
	}
}

func TestProbesAreAnswered(t *testing.T) {
	b, s := newBridge(true)
	b.Write([]byte("\x1b7\x1b_Gi=4207,a=q,t=d,f=24,s=1,v=1;AAAA\x1b\\\x1b[c"))
	if got := s.replies.String(); got != "\x1b_Gi=4207;OK\x1b\\" {
		t.Fatalf("kitty probe reply %q", got)
	}
	if got := s.out.String(); got != "\x1b7\x1b[c" {
		t.Fatalf("forwarded %q; the kitty query must be swallowed and DA1 forwarded", got)
	}
	s.replies.Reset()
	b.Write([]byte("\x1b_Gi=299,a=q,t=s,f=32,s=10,v=10;L3B4LXByb2Jl\x1b\\"))
	if got := s.replies.String(); !strings.HasPrefix(got, "\x1b_Gi=299;EBADF") {
		t.Fatalf("shared memory probe must fail, got %q", got)
	}
	s.replies.Reset()
	b.Write([]byte("\x1b[16t\x1b[14t\x1b[?1016$p"))
	if got := s.replies.String(); got != "\x1b[6;20;10t\x1b[4;80;80t\x1b[?1016;0$y" {
		t.Fatalf("size replies %q", got)
	}
}

func TestFullFrameThenPatch(t *testing.T) {
	b, s := newBridge(true)
	// Full frame, like pixel-kitty's Full presenter.
	frame := "\x1b[?2026h\x1b[H" + transmit("a=T,f=32,o=z,s=80,v=80,t=d,i=1,p=1,C=1,z=0,q=2", solid(80, 80, 255, 255, 255)) + "\x1b[?2026l"
	b.Write([]byte(frame))
	out := s.out.String()
	if !strings.Contains(out, "\x1b[?80h\x1bP0;1;0q\"1;1;80;80") {
		t.Fatalf("no full sixel image in output: %q", out)
	}
	if strings.Contains(out, "\x1b_G") {
		t.Fatal("kitty commands must not reach the terminal")
	}
	sixelAt := strings.Index(out, "\x1bP")
	syncEnd := strings.LastIndex(out, "\x1b[?2026l")
	if sixelAt < 0 || syncEnd < sixelAt {
		t.Fatal("sixel must be written before the synchronized frame ends")
	}
	if s.replies.Len() != 0 {
		t.Fatalf("q=2 must stay silent, got %q", s.replies.String())
	}

	// A 5x5 red patch at pixel 23,24 (cell 2,1 offset 3,4).
	s.out.Reset()
	patch := "\x1b[?2026h\x1b[2;3H" + transmit("a=T,f=32,o=z,s=5,v=5,t=d,i=2,p=1,C=1,z=2,X=3,Y=4,q=2", solid(5, 5, 255, 0, 0)) + "\x1b[?2026l"
	b.Write([]byte(patch))
	out = s.out.String()
	if !strings.Contains(out, "\"1;1;28;30") {
		t.Fatalf("patch image should end at x=28, band through y=30: %q", out)
	}
	if len(out) > 400 {
		t.Fatalf("patch output too large (%d bytes): %q", len(out), out)
	}

	// Deleting the patch restores the white base underneath.
	s.out.Reset()
	b.Write([]byte("\x1b[?2026h\x1b_Ga=d,d=I,i=2,q=2\x1b\\\x1b[?2026l"))
	if !strings.Contains(s.out.String(), "#255;2;100;100;100") {
		t.Fatalf("base not repainted after delete: %q", s.out.String())
	}
}

func TestTextOverImageIsRepainted(t *testing.T) {
	b, s := newBridge(true)
	b.Write([]byte("\x1b[?2026h\x1b[H" + transmit("a=T,f=32,o=z,s=80,v=80,t=d,i=1,p=1,C=1,q=2", solid(80, 80, 0, 0, 255)) + "\x1b[?2026l"))
	s.out.Reset()
	b.Write([]byte("\x1b[3;1Hhi"))
	time.Sleep(50 * time.Millisecond) // paced render runs on a timer
	b.mu.Lock()
	out := s.out.String()
	b.mu.Unlock()
	if !strings.HasPrefix(out, "\x1b[3;1Hhi") || !strings.Contains(out, "\x1bP") {
		t.Fatalf("row 3 should be repainted after text: %q", out)
	}
}

func TestInBandResizeEmulated(t *testing.T) {
	b, s := newBridge(true)
	b.Write([]byte("\x1b[?1049;2048h"))
	if s.out.String() != "\x1b[?1049h" {
		t.Fatalf("2048 must be stripped, forwarded %q", s.out.String())
	}
	if s.replies.String() != "\x1b[48;4;8;80;80t" {
		t.Fatalf("initial size report %q", s.replies.String())
	}
	s.replies.Reset()
	b.Resize(10, 5)
	if s.replies.String() != "\x1b[48;5;10;100;100t" {
		t.Fatalf("resize report %q", s.replies.String())
	}
}

func TestCursorMovesWithoutC1(t *testing.T) {
	b, s := newBridge(true)
	b.Write([]byte("\x1b[1;1H" + transmit("a=T,f=32,o=z,s=25,v=30,t=d,i=7,q=2", solid(25, 30, 1, 2, 3))))
	// 25x30 px = 3 columns, 2 rows: one LF and three columns right.
	if !strings.Contains(s.out.String(), "\n\x1b[3C") {
		t.Fatalf("cursor movement missing: %q", s.out.String())
	}
	if b.screen.X != 3 || b.screen.Y != 1 {
		t.Fatalf("tracked cursor %d,%d", b.screen.X, b.screen.Y)
	}
}
