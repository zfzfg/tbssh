package vtstream

import "unicode/utf8"

// Screen follows the remote program's cursor and the few modes tbssh cares
// about. It is deliberately small: it does not keep the cell contents, only
// what is needed to anchor kitty images at the cursor and to know which rows
// the terminal just overwrote with text (Windows Terminal wipes sixel pixels
// in cells that receive text).
type Screen struct {
	Cols, Rows int
	X, Y       int // cursor, 0-based

	pendingWrap bool
	savedX      int
	savedY      int
	top, bottom int // scroll region, inclusive, 0-based
	originMode  bool
	autoWrap    bool

	// AltScreen is true while the alternate screen buffer is active.
	AltScreen bool
	// Synchronized is true between CSI ?2026h and CSI ?2026l.
	Synchronized bool

	utf8buf []byte

	// Events, consumed by the owner after each token.
	touchedTop, touchedBottom int // rows written or erased, -1 when none
	scrolled                  int // lines scrolled up (positive) or down (negative) in the region
	scrollTop, scrollBottom   int
	cleared                   bool // the whole screen was cleared or switched
}

// NewScreen returns a tracker for a cols x rows screen.
func NewScreen(cols, rows int) *Screen {
	s := &Screen{autoWrap: true}
	s.Resize(cols, rows)
	s.ResetEvents()
	return s
}

// Resize changes the screen size, like a terminal resize.
func (s *Screen) Resize(cols, rows int) {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	s.Cols, s.Rows = cols, rows
	s.top, s.bottom = 0, rows-1
	s.clamp()
	s.pendingWrap = false
}

// Events reports what the last tokens did since ResetEvents.
type Events struct {
	TouchedTop, TouchedBottom int // -1 when no row was written
	Scrolled                  int // >0 scrolled up, <0 scrolled down
	ScrollTop, ScrollBottom   int
	Cleared                   bool
}

// TakeEvents returns and resets the accumulated events.
func (s *Screen) TakeEvents() Events {
	e := Events{
		TouchedTop: s.touchedTop, TouchedBottom: s.touchedBottom,
		Scrolled: s.scrolled, ScrollTop: s.scrollTop, ScrollBottom: s.scrollBottom,
		Cleared: s.cleared,
	}
	s.ResetEvents()
	return e
}

// ResetEvents forgets accumulated events.
func (s *Screen) ResetEvents() {
	s.touchedTop, s.touchedBottom = -1, -1
	s.scrolled = 0
	s.cleared = false
}

func (s *Screen) touch(r0, r1 int) {
	if r0 < 0 {
		r0 = 0
	}
	if r1 > s.Rows-1 {
		r1 = s.Rows - 1
	}
	if r0 > r1 {
		return
	}
	if s.touchedTop < 0 || r0 < s.touchedTop {
		s.touchedTop = r0
	}
	if r1 > s.touchedBottom {
		s.touchedBottom = r1
	}
}

func (s *Screen) scroll(n, top, bottom int) {
	if n == 0 {
		return
	}
	if s.scrolled != 0 && (s.scrollTop != top || s.scrollBottom != bottom) {
		// Two different regions scrolled: report the union as touched.
		s.touch(s.scrollTop, s.scrollBottom)
		s.touch(top, bottom)
		return
	}
	s.scrolled += n
	s.scrollTop, s.scrollBottom = top, bottom
	s.touch(top, bottom)
}

func (s *Screen) clamp() {
	if s.X < 0 {
		s.X = 0
	}
	if s.X > s.Cols-1 {
		s.X = s.Cols - 1
	}
	if s.Y < 0 {
		s.Y = 0
	}
	if s.Y > s.Rows-1 {
		s.Y = s.Rows - 1
	}
}

// Apply updates the tracker with one token.
func (s *Screen) Apply(t *Token) {
	switch t.Kind {
	case Text:
		s.text(t.Raw)
	case Esc:
		s.esc(t)
	case CSI:
		s.csi(t)
	}
}

func (s *Screen) lineFeed() {
	s.pendingWrap = false
	if s.Y == s.bottom {
		s.scroll(1, s.top, s.bottom)
		return
	}
	if s.Y < s.Rows-1 {
		s.Y++
	}
}

func (s *Screen) reverseIndex() {
	s.pendingWrap = false
	if s.Y == s.top {
		s.scroll(-1, s.top, s.bottom)
		return
	}
	if s.Y > 0 {
		s.Y--
	}
}

func (s *Screen) text(raw []byte) {
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if b < 0x20 || b == 0x7f {
			s.utf8buf = s.utf8buf[:0]
			switch b {
			case '\r':
				s.X = 0
				s.pendingWrap = false
			case '\n', 0x0b, 0x0c:
				s.lineFeed()
			case '\b':
				if s.X > 0 {
					s.X--
				}
				s.pendingWrap = false
			case '\t':
				next := (s.X/8 + 1) * 8
				if next > s.Cols-1 {
					next = s.Cols - 1
				}
				s.X = next
			}
			continue
		}
		if b < 0x80 {
			s.utf8buf = s.utf8buf[:0]
			s.put(1)
			continue
		}
		// UTF-8: collect a whole rune, then advance by its width.
		if utf8.RuneStart(b) {
			s.utf8buf = append(s.utf8buf[:0], b)
		} else {
			if len(s.utf8buf) == 0 {
				continue
			}
			s.utf8buf = append(s.utf8buf, b)
		}
		if utf8.FullRune(s.utf8buf) {
			r, _ := utf8.DecodeRune(s.utf8buf)
			s.utf8buf = s.utf8buf[:0]
			if w := RuneWidth(r); w > 0 {
				s.put(w)
			}
		}
	}
}

func (s *Screen) put(w int) {
	if s.pendingWrap && s.autoWrap {
		s.X = 0
		s.lineFeed()
	}
	s.pendingWrap = false
	if w == 2 && s.X == s.Cols-1 && s.autoWrap {
		s.X = 0
		s.lineFeed()
	}
	s.touch(s.Y, s.Y)
	s.X += w
	if s.X >= s.Cols {
		s.X = s.Cols - 1
		s.pendingWrap = true
	}
}

func (s *Screen) esc(t *Token) {
	if len(t.Inter) != 0 {
		return
	}
	switch t.Final {
	case '7':
		s.savedX, s.savedY = s.X, s.Y
	case '8':
		s.X, s.Y = s.savedX, s.savedY
		s.clamp()
		s.pendingWrap = false
	case 'D':
		s.lineFeed()
	case 'E':
		s.X = 0
		s.lineFeed()
	case 'M':
		s.reverseIndex()
	case 'c':
		cols, rows := s.Cols, s.Rows
		*s = Screen{autoWrap: true}
		s.Resize(cols, rows)
		s.ResetEvents()
		s.cleared = true
	}
}

func (s *Screen) csi(t *Token) {
	if len(t.Inter) != 0 {
		return
	}
	if t.Private == '?' {
		if t.Final == 'h' || t.Final == 'l' {
			set := t.Final == 'h'
			for i := 0; i < t.NumParams(); i++ {
				s.mode(t.Param(i, 0), set)
			}
		}
		return
	}
	if t.Private != 0 {
		return
	}
	n := t.Param(0, 1)
	if n < 1 {
		n = 1
	}
	switch t.Final {
	case 'A':
		s.Y -= n
		if s.Y < s.top && s.Y+n >= s.top {
			s.Y = s.top
		}
		s.clamp()
		s.pendingWrap = false
	case 'B', 'e':
		s.Y += n
		if s.Y > s.bottom && s.Y-n <= s.bottom {
			s.Y = s.bottom
		}
		s.clamp()
		s.pendingWrap = false
	case 'C', 'a':
		s.X += n
		s.clamp()
		s.pendingWrap = false
	case 'D':
		s.X -= n
		s.clamp()
		s.pendingWrap = false
	case 'E':
		s.X = 0
		s.Y += n
		s.clamp()
		s.pendingWrap = false
	case 'F':
		s.X = 0
		s.Y -= n
		s.clamp()
		s.pendingWrap = false
	case 'G', '`':
		s.X = n - 1
		s.clamp()
		s.pendingWrap = false
	case 'd':
		s.Y = n - 1
		if s.originMode {
			s.Y += s.top
		}
		s.clamp()
		s.pendingWrap = false
	case 'H', 'f':
		row := t.Param(0, 1)
		col := t.Param(1, 1)
		if row < 1 {
			row = 1
		}
		if col < 1 {
			col = 1
		}
		s.Y = row - 1
		if s.originMode {
			s.Y += s.top
		}
		s.X = col - 1
		s.clamp()
		s.pendingWrap = false
	case 'J':
		switch t.Param(0, 0) {
		case 0:
			s.touch(s.Y, s.Rows-1)
		case 1:
			s.touch(0, s.Y)
		case 2, 3:
			s.touch(0, s.Rows-1)
			s.cleared = true
		}
	case 'K', 'X', 'P', '@':
		s.touch(s.Y, s.Y)
	case 'L', 'M':
		if s.Y >= s.top && s.Y <= s.bottom {
			s.touch(s.Y, s.bottom)
		}
	case 'S':
		s.scroll(n, s.top, s.bottom)
	case 'T':
		s.scroll(-n, s.top, s.bottom)
	case 'r':
		top := t.Param(0, 1) - 1
		bottom := t.Param(1, s.Rows) - 1
		if bottom > s.Rows-1 || bottom <= 0 {
			bottom = s.Rows - 1
		}
		if top < 0 {
			top = 0
		}
		if top < bottom {
			s.top, s.bottom = top, bottom
		}
		s.X = 0
		s.Y = 0
		if s.originMode {
			s.Y = s.top
		}
		s.pendingWrap = false
	case 's':
		if t.NumParams() == 0 {
			s.savedX, s.savedY = s.X, s.Y
		}
	case 'u':
		s.X, s.Y = s.savedX, s.savedY
		s.clamp()
		s.pendingWrap = false
	}
}

func (s *Screen) mode(m int, set bool) {
	switch m {
	case 6:
		s.originMode = set
		s.X, s.Y = 0, 0
		if set {
			s.Y = s.top
		}
	case 7:
		s.autoWrap = set
	case 47, 1047, 1049:
		if s.AltScreen != set {
			s.AltScreen = set
			s.cleared = true
		}
	case 2026:
		s.Synchronized = set
	}
}

// RuneWidth returns how many cells a terminal uses for r: 0 for combining
// marks and zero-width characters, 2 for East Asian wide characters and most
// emoji, 1 otherwise. It is an approximation that is good enough to follow
// the cursor.
func RuneWidth(r rune) int {
	switch {
	case r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff:
		return 0
	case r >= 0x0300 && r <= 0x036f, r >= 0x1ab0 && r <= 0x1aff, r >= 0x1dc0 && r <= 0x1dff,
		r >= 0x20d0 && r <= 0x20ff, r >= 0xfe20 && r <= 0xfe2f, r >= 0xfe00 && r <= 0xfe0f:
		return 0
	case r >= 0x1100 && r <= 0x115f,
		r >= 0x2e80 && r <= 0x303e,
		r >= 0x3041 && r <= 0x33ff,
		r >= 0x3400 && r <= 0x4dbf,
		r >= 0x4e00 && r <= 0x9fff,
		r >= 0xa000 && r <= 0xa4cf,
		r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff,
		r >= 0xfe30 && r <= 0xfe4f,
		r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f64f,
		r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x1fa70 && r <= 0x1faff,
		r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}
