package app

import (
	"io"
	"sync"
)

// outQueue writes to an underlying writer from its own goroutine, so the
// caller (the SSH reader and the bridge) keeps parsing and composing the
// next frame while the terminal is still busy decoding the previous sixel
// image. Bytes keep their order. Write blocks only when more than limit
// bytes are waiting, which bounds memory if the terminal falls behind.
type outQueue struct {
	w     io.Writer
	limit int

	mu      sync.Mutex
	cond    *sync.Cond
	pending []byte
	spare   []byte
	closed  bool
	err     error
	done    chan struct{}
}

func newOutQueue(w io.Writer, limit int) *outQueue {
	q := &outQueue{w: w, limit: limit, done: make(chan struct{})}
	q.cond = sync.NewCond(&q.mu)
	go q.run()
	return q
}

func (q *outQueue) Write(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.pending) > q.limit && !q.closed && q.err == nil {
		q.cond.Wait()
	}
	if q.err != nil {
		return 0, q.err
	}
	if q.closed {
		return 0, io.ErrClosedPipe
	}
	q.pending = append(q.pending, p...)
	q.cond.Broadcast()
	return len(p), nil
}

func (q *outQueue) run() {
	defer close(q.done)
	q.mu.Lock()
	for {
		for len(q.pending) == 0 && !q.closed {
			q.cond.Wait()
		}
		if len(q.pending) == 0 {
			q.mu.Unlock()
			return
		}
		buf := q.pending
		q.pending = q.spare[:0]
		q.mu.Unlock()

		_, err := q.w.Write(buf)

		q.mu.Lock()
		q.spare = buf[:0]
		if err != nil && q.err == nil {
			q.err = err
			q.pending = nil
		}
		q.cond.Broadcast()
	}
}

// Close writes what is still queued and stops the writer goroutine.
func (q *outQueue) Close() error {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
	<-q.done
	return q.err
}
