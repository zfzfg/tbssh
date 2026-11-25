package sixel

import (
	"bytes"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

// terminal is a tiny sixel decoder that paints images at the top-left
// corner over its existing pixels, like Windows Terminal in display mode.
type terminal struct {
	w, h int
	pix  []int // palette index per pixel, -1 = nothing
}

func newTerminal(w, h int) *terminal {
	t := &terminal{w: w, h: h, pix: make([]int, w*h)}
	for i := range t.pix {
		t.pix[i] = -1
	}
	return t
}

func (t *terminal) apply(tb testing.TB, seq []byte) {
	s := string(seq)
	if !strings.HasPrefix(s, "\x1b[?80h\x1bP0;1;0q\"1;1;") || !strings.HasSuffix(s, "\x1b\\\x1b[?80l") {
		tb.Fatalf("unexpected framing: %q", s)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(s, "\x1b[?80h\x1bP0;1;0q"), "\x1b\\\x1b[?80l")
	i := 0
	num := func() int {
		j := i
		for i < len(body) && body[i] >= '0' && body[i] <= '9' {
			i++
		}
		n, _ := strconv.Atoi(body[j:i])
		return n
	}
	// raster attributes
	i++ // "
	num()
	i++
	num()
	i++
	num()
	i++
	num()
	x, y, color := 0, 0, 0
	for i < len(body) {
		c := body[i]
		switch {
		case c == '#':
			i++
			color = num()
			if i < len(body) && body[i] == ';' {
				for k := 0; k < 4; k++ {
					i++
					num()
				}
			}
		case c == '$':
			x = 0
			i++
		case c == '-':
			x = 0
			y += 6
			i++
		case c == '!':
			i++
			n := num()
			ch := body[i]
			i++
			for k := 0; k < n; k++ {
				t.put(x, y, color, ch)
				x++
			}
		case c >= 63 && c <= 126:
			t.put(x, y, color, c)
			x++
			i++
		default:
			tb.Fatalf("unexpected byte %q at %d", c, i)
		}
	}
}

func (t *terminal) put(x, y, color int, ch byte) {
	bits := ch - 63
	for k := 0; k < 6; k++ {
		if bits&(1<<k) != 0 && x < t.w && y+k < t.h {
			t.pix[(y+k)*t.w+x] = color
		}
	}
}

func TestRoundTripAgainstDecoder(t *testing.T) {
	const w, h = 37, 23
	rng := rand.New(rand.NewSource(3))
	canvas := make([]byte, w*h*4)
	covered := make([]bool, w*h)
	s := NewScreen(w, h)
	term := newTerminal(w, h)
	bg := [3]byte{12, 12, 12}

	for frame := 0; frame < 40; frame++ {
		// Change a random rectangle.
		r := Rect{rng.Intn(w), rng.Intn(h), 1 + rng.Intn(w), 1 + rng.Intn(h)}
		for y := r.Y; y < min(r.Y+r.H, h); y++ {
			for x := r.X; x < min(r.X+r.W, w); x++ {
				at := y*w + x
				if rng.Intn(5) == 0 {
					covered[at] = false
					continue
				}
				covered[at] = true
				canvas[at*4], canvas[at*4+1], canvas[at*4+2] = byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))
			}
		}
		if seq := s.Update(canvas, covered, []Rect{r}, bg); seq != nil {
			term.apply(t, seq)
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				at := y*w + x
				if !covered[at] {
					continue
				}
				want := int(Quantize(canvas[at*4], canvas[at*4+1], canvas[at*4+2], x, y))
				if term.pix[at] != want {
					t.Fatalf("frame %d: pixel %d,%d shows %d, want %d", frame, x, y, term.pix[at], want)
				}
			}
		}
	}
}

func TestUnchangedSendsNothing(t *testing.T) {
	const w, h = 10, 6
	canvas := bytes.Repeat([]byte{255, 255, 255, 255}, w*h)
	covered := make([]bool, w*h)
	for i := range covered {
		covered[i] = true
	}
	s := NewScreen(w, h)
	first := s.Update(canvas, covered, []Rect{{0, 0, w, h}}, [3]byte{})
	if want := "\x1b[?80h\x1bP0;1;0q\"1;1;10;6#255;2;100;100;100#255!10~\x1b\\\x1b[?80l"; string(first) != want {
		t.Fatalf("first frame\n got %q\nwant %q", first, want)
	}
	if again := s.Update(canvas, covered, []Rect{{0, 0, w, h}}, [3]byte{}); again != nil {
		t.Fatalf("unchanged frame sent %q", again)
	}
	s.ForgetRows(0, 1)
	if again := s.Update(canvas, covered, []Rect{{0, 0, w, h}}, [3]byte{}); !bytes.Contains(again, []byte("!10@")) {
		t.Fatalf("forgotten row not repainted: %q", again)
	}
}

func TestUncoveredPixelIsErasedOnce(t *testing.T) {
	const w, h = 4, 6
	canvas := bytes.Repeat([]byte{255, 0, 0, 255}, w*h)
	covered := []bool{true, true, true, true}
	covered = append(covered, make([]bool, w*h-4)...)
	s := NewScreen(w, h)
	s.Update(canvas, covered, []Rect{{0, 0, w, h}}, [3]byte{})
	covered[1] = false
	seq := s.Update(canvas, covered, []Rect{{0, 0, w, h}}, [3]byte{})
	if !bytes.Contains(seq, []byte("#216")) {
		t.Fatalf("expected background (index 216) paint, got %q", seq)
	}
	if again := s.Update(canvas, covered, []Rect{{0, 0, w, h}}, [3]byte{}); again != nil {
		t.Fatalf("background painted twice: %q", again)
	}
}

func BenchmarkFullFrame(b *testing.B) {
	const w, h = 2000, 1000
	canvas := make([]byte, w*h*4)
	rng := rand.New(rand.NewSource(1))
	covered := make([]bool, w*h)
	for i := range covered {
		covered[i] = true
		// Mostly flat areas with some noise, like a web page.
		v := byte(240)
		if rng.Intn(20) == 0 {
			v = byte(rng.Intn(256))
		}
		canvas[i*4], canvas[i*4+1], canvas[i*4+2] = v, v, byte(rng.Intn(3)*100)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := NewScreen(w, h)
		out := s.Update(canvas, covered, []Rect{{0, 0, w, h}}, [3]byte{})
		b.SetBytes(int64(len(out)))
	}
}
