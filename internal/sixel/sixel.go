// Package sixel turns a composed canvas into sixel images for Windows
// Terminal.
//
// Every update is one transparent sixel image anchored at the top-left
// corner of the screen (sixel display mode, DECSDM), holding only the
// pixels whose palette index differs from what the terminal already shows.
// Transparent pixels leave the terminal's earlier pixels alone, so a small
// change costs a small image, and display mode keeps images from scrolling
// the screen or moving the cursor.
//
// The palette is fixed (a 6x6x6 cube with ordered dithering plus a 40-step
// gray ramp) so the same color always maps to the same index and unchanged
// pixels can be skipped. The approach follows the sixel output proposed for
// terminal-browser's own Windows support (zenbu-labs/terminal-browser#205).
package sixel

import (
	"strconv"
)

const (
	cubeLevels = 6
	cubeSize   = 216
	grayLevels = 40
	// PaletteSize is the number of color registers used.
	PaletteSize = cubeSize + grayLevels
	// Pixels whose channels spread less than this are drawn from the gray
	// ramp, which keeps antialiased text crisp.
	grayChroma = 12
	band       = 6

	notShown = 0xffff
)

var bayer = [4][4]uint32{{0, 8, 2, 10}, {12, 4, 14, 6}, {3, 11, 1, 9}, {15, 7, 13, 5}}

func cubeLevel(v uint8, threshold uint32) int {
	l := (uint32(v)*(cubeLevels-1)*16 + threshold*255) / (255 * 16)
	if l > cubeLevels-1 {
		l = cubeLevels - 1
	}
	return int(l)
}

// Quantize maps a color at screen position x,y to a palette index.
func Quantize(r, g, b uint8, x, y int) uint16 {
	hi := max(r, g, b)
	lo := min(r, g, b)
	if hi-lo < grayChroma {
		luma := (uint32(r) + uint32(g) + uint32(b)) / 3
		return uint16(cubeSize + (luma*(grayLevels-1)+127)/255)
	}
	t := bayer[y&3][x&3]
	return uint16(cubeLevel(r, t)*36 + cubeLevel(g, t)*6 + cubeLevel(b, t))
}

// PaletteRGB returns a palette entry's color.
func PaletteRGB(i uint16) (r, g, b int) {
	if i < cubeSize {
		level := func(l int) int { return l * 255 / (cubeLevels - 1) }
		return level(int(i) / 36), level(int(i) / 6 % 6), level(int(i) % 6)
	}
	gray := int(i-cubeSize) * 255 / (grayLevels - 1)
	return gray, gray, gray
}

// Screen remembers which palette index the terminal shows at every pixel.
type Screen struct {
	W, H  int
	shown []uint16

	// scratch
	runs [PaletteSize][]run
}

type run struct {
	x    int
	bits uint8
}

// NewScreen returns a screen of w x h pixels showing nothing.
func NewScreen(w, h int) *Screen {
	s := &Screen{}
	s.Resize(w, h)
	return s
}

// Resize forgets everything shown and changes the size.
func (s *Screen) Resize(w, h int) {
	s.W, s.H = w, h
	s.shown = make([]uint16, w*h)
	s.Forget()
}

// Forget marks every pixel as not shown, for example after the terminal
// cleared its screen.
func (s *Screen) Forget() {
	for i := range s.shown {
		s.shown[i] = notShown
	}
}

// ForgetRows marks pixel rows y0..y1-1 as not shown, for example because
// the terminal wrote text there and wiped the image.
func (s *Screen) ForgetRows(y0, y1 int) {
	y0, y1 = max(y0, 0), min(y1, s.H)
	for i := y0 * s.W; i < y1*s.W; i++ {
		s.shown[i] = notShown
	}
}

// Rect is a pixel rectangle.
type Rect struct{ X, Y, W, H int }

// Update builds one sixel image (including DECSDM on/off around it) that
// brings the terminal in line with canvas inside the damaged rectangles.
//
// canvas is opaque RGBA of the screen size; covered tells which pixels
// belong to an image. Pixels that are not covered but were shown before are
// painted with the background color bg, then forgotten. It returns nil when
// nothing needs to be sent.
func (s *Screen) Update(canvas []byte, covered []bool, damage []Rect, bg [3]byte) []byte {
	if len(damage) == 0 {
		return nil
	}
	clipped := damage[:0:0]
	for _, r := range damage {
		x0, y0 := max(r.X, 0), max(r.Y, 0)
		x1, y1 := min(r.X+r.W, s.W), min(r.Y+r.H, s.H)
		if x1 > x0 && y1 > y0 {
			clipped = append(clipped, Rect{x0, y0, x1 - x0, y1 - y0})
		}
	}
	if len(clipped) == 0 {
		return nil
	}

	var used [PaletteSize]bool
	right := 0
	lastBand := -1
	bands := make([][]byte, 0, (s.H+band-1)/band)

	for bi := 0; bi*band < s.H; bi++ {
		top := bi * band
		bottom := min(top+band, s.H)
		x0, x1 := -1, -1
		for _, r := range clipped {
			if r.Y < bottom && r.Y+r.H > top {
				if x0 < 0 || r.X < x0 {
					x0 = r.X
				}
				if r.X+r.W > x1 {
					x1 = r.X + r.W
				}
			}
		}
		if x0 < 0 {
			bands = append(bands, nil)
			continue
		}
		for i := range s.runs {
			s.runs[i] = s.runs[i][:0]
		}
		any := false
		// Column by column, so each color's runs come out sorted by x and the
		// six rows of a column merge into one sixel.
		for x := x0; x < x1; x++ {
			for y := top; y < bottom; y++ {
				bit := uint8(1) << uint(y-top)
				at := y*s.W + x
				var idx uint16
				if covered[at] {
					p := canvas[at*4 : at*4+3]
					idx = Quantize(p[0], p[1], p[2], x, y)
					if s.shown[at] == idx {
						continue
					}
					s.shown[at] = idx
				} else {
					if s.shown[at] == notShown {
						continue
					}
					idx = Quantize(bg[0], bg[1], bg[2], x, y)
					s.shown[at] = notShown
				}
				list := s.runs[idx]
				if n := len(list); n > 0 && list[n-1].x == x {
					list[n-1].bits |= bit
				} else {
					list = append(list, run{x, bit})
				}
				s.runs[idx] = list
				any = true
			}
		}
		if !any {
			bands = append(bands, nil)
			continue
		}
		var body []byte
		for idx := range s.runs {
			list := s.runs[idx]
			if len(list) == 0 {
				continue
			}
			used[idx] = true
			if end := list[len(list)-1].x + 1; end > right {
				right = end
			}
			if len(body) > 0 {
				body = append(body, '$')
			}
			body = append(body, '#')
			body = strconv.AppendInt(body, int64(idx), 10)
			cursor := 0
			var cur byte
			count := 0
			for _, r := range list {
				ch := byte(63 + r.bits)
				if count > 0 && cur == ch && cursor == r.x {
					count++
				} else {
					body = pushRun(body, cur, count)
					body = pushRun(body, '?', r.x-cursor)
					cur, count = ch, 1
				}
				cursor = r.x + 1
			}
			body = pushRun(body, cur, count)
		}
		lastBand = bi
		bands = append(bands, body)
	}
	if lastBand < 0 {
		return nil
	}

	height := min((lastBand+1)*band, s.H)
	out := make([]byte, 0, 4096)
	out = append(out, "\x1b[?80h\x1bP0;1;0q\"1;1;"...)
	out = strconv.AppendInt(out, int64(right), 10)
	out = append(out, ';')
	out = strconv.AppendInt(out, int64(height), 10)
	for idx, u := range used {
		if !u {
			continue
		}
		r, g, b := PaletteRGB(uint16(idx))
		out = append(out, '#')
		out = strconv.AppendInt(out, int64(idx), 10)
		out = append(out, ";2;"...)
		out = strconv.AppendInt(out, int64(r*100/255), 10)
		out = append(out, ';')
		out = strconv.AppendInt(out, int64(g*100/255), 10)
		out = append(out, ';')
		out = strconv.AppendInt(out, int64(b*100/255), 10)
	}
	for bi := 0; bi <= lastBand; bi++ {
		if bi > 0 {
			out = append(out, '-')
		}
		out = append(out, bands[bi]...)
	}
	out = append(out, "\x1b\\\x1b[?80l"...)
	return out
}

func pushRun(out []byte, ch byte, count int) []byte {
	switch {
	case count <= 0:
		return out
	case count <= 3:
		for i := 0; i < count; i++ {
			out = append(out, ch)
		}
		return out
	default:
		out = append(out, '!')
		out = strconv.AppendInt(out, int64(count), 10)
		return append(out, ch)
	}
}
