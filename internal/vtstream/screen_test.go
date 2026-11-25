package vtstream

import "testing"

func feed(s *Screen, data string) {
	var p Parser
	p.Feed([]byte(data), func(t Token) { s.Apply(&t) })
}

func TestCursorMoves(t *testing.T) {
	s := NewScreen(80, 24)
	feed(s, "\x1b[5;10H")
	if s.X != 9 || s.Y != 4 {
		t.Fatalf("CUP: got %d,%d", s.X, s.Y)
	}
	feed(s, "abc")
	if s.X != 12 {
		t.Fatalf("text: got x=%d", s.X)
	}
	feed(s, "\r\n\x1b[3C")
	if s.X != 3 || s.Y != 5 {
		t.Fatalf("CR LF CUF: got %d,%d", s.X, s.Y)
	}
	feed(s, "\x1b7\x1b[H\x1b8")
	if s.X != 3 || s.Y != 5 {
		t.Fatalf("save/restore: got %d,%d", s.X, s.Y)
	}
	feed(s, "漢")
	if s.X != 5 {
		t.Fatalf("wide char: got x=%d", s.X)
	}
}

func TestWrapAndScroll(t *testing.T) {
	s := NewScreen(4, 2)
	s.TakeEvents()
	feed(s, "abcd")
	if s.X != 3 || s.Y != 0 {
		t.Fatalf("pending wrap position: %d,%d", s.X, s.Y)
	}
	feed(s, "e")
	if s.X != 1 || s.Y != 1 {
		t.Fatalf("after wrap: %d,%d", s.X, s.Y)
	}
	feed(s, "\n")
	ev := s.TakeEvents()
	if ev.Scrolled != 1 {
		t.Fatalf("expected one line scrolled, got %+v", ev)
	}
}

func TestModesAndEvents(t *testing.T) {
	s := NewScreen(80, 24)
	s.TakeEvents()
	feed(s, "\x1b[?1049h")
	if !s.AltScreen || !s.TakeEvents().Cleared {
		t.Fatal("alt screen not tracked")
	}
	feed(s, "\x1b[?2026h")
	if !s.Synchronized {
		t.Fatal("sync not tracked")
	}
	feed(s, "\x1b[3;1Hxx")
	ev := s.TakeEvents()
	if ev.TouchedTop != 2 || ev.TouchedBottom != 2 {
		t.Fatalf("touched rows %+v", ev)
	}
	feed(s, "\x1b[2J")
	if !s.TakeEvents().Cleared {
		t.Fatal("ED2 not reported")
	}
	feed(s, "\x1b[H")
	if ev := s.TakeEvents(); ev.TouchedTop != -1 {
		t.Fatalf("cursor move must not touch rows: %+v", ev)
	}
}
