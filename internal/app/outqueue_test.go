package app

import (
	"bytes"
	"fmt"
	"testing"
)

func TestOutQueueKeepsOrder(t *testing.T) {
	var got bytes.Buffer
	var want bytes.Buffer
	q := newOutQueue(&got, 64)
	for i := 0; i < 2000; i++ {
		s := fmt.Sprintf("chunk %d;", i)
		want.WriteString(s)
		q.Write([]byte(s))
	}
	q.Close()
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Fatal("output differs from input")
	}
}

type slowWriter struct {
	calls  [][]byte
	gate   chan struct{}
	called chan struct{}
}

func (w *slowWriter) Write(p []byte) (int, error) {
	w.calls = append(w.calls, append([]byte(nil), p...))
	w.called <- struct{}{}
	<-w.gate
	return len(p), nil
}

// What piles up while the terminal is busy goes out in one write, so a
// sixel image queued in pieces is never split across writes.
func TestOutQueueCoalesces(t *testing.T) {
	w := &slowWriter{gate: make(chan struct{}, 10), called: make(chan struct{}, 10)}
	q := newOutQueue(w, 1<<20)
	q.Write([]byte("first"))
	<-w.called // the writer is now blocked on "first"
	q.Write([]byte("\x1bP0;1;0q"))
	q.Write([]byte("#1!5~"))
	q.Write([]byte("\x1b\\"))
	w.gate <- struct{}{}
	<-w.called
	w.gate <- struct{}{}
	q.Close()
	if len(w.calls) != 2 || string(w.calls[1]) != "\x1bP0;1;0q#1!5~\x1b\\" {
		t.Fatalf("writes %q", w.calls)
	}
}
