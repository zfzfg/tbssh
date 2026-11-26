package app

import "testing"

func TestEscaper(t *testing.T) {
	e := newEscaper("~")
	out, act := e.filter([]byte("a~b"))
	if string(out) != "a~b" || act != escNone {
		t.Fatalf("mid-line tilde must pass: %q %d", out, act)
	}
	out, act = e.filter([]byte("\r~"))
	if string(out) != "\r" || act != escNone {
		t.Fatalf("tilde after CR must be held: %q", out)
	}
	out, act = e.filter([]byte("~x"))
	if string(out) != "~x" {
		t.Fatalf("~~ should send one tilde then x: %q", out)
	}
	out, act = e.filter([]byte("\r~."))
	if string(out) != "\r" || act != escDisconnect {
		t.Fatalf("~. must disconnect: %q %d", out, act)
	}
	e = newEscaper("none")
	out, _ = e.filter([]byte("~."))
	if string(out) != "~." {
		t.Fatal("escape none must pass everything")
	}
}

func TestEscaperRedraw(t *testing.T) {
	e := newEscaper("~")
	out, act := e.filter([]byte("~r"))
	if len(out) != 0 || act != escRedraw {
		t.Fatalf("~r must redraw: %q %d", out, act)
	}
	out, act = e.filter([]byte("a~r"))
	if string(out) != "a~r" || act != escNone {
		t.Fatalf("mid-line ~r must pass: %q %d", out, act)
	}
}
