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
