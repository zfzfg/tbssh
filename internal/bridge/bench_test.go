package bridge

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"testing"
)

// BenchmarkFullFrame sends a full-screen RGBA frame that changes every
// iteration, like a scrolling web page.
func BenchmarkFullFrame(b *testing.B) {
	const cols, rows, cw, ch = 160, 45, 10, 20
	w, h := cols*cw, rows*ch
	br := New(io.Discard, nil, Options{Cols: cols, Rows: rows, CellW: cw, CellH: ch, Graphics: true, MaxFPS: 1 << 20})
	pix := make([]byte, w*h*4)
	frames := make([][]byte, 2)
	for f := range frames {
		for i := 0; i < w*h; i++ {
			x, y := i%w, i/w
			pix[i*4], pix[i*4+1], pix[i*4+2], pix[i*4+3] = byte(x+f*37), byte(y*3), byte((x^y)+f*91), 255
		}
		var buf bytes.Buffer
		enc := base64.StdEncoding.EncodeToString(pix)
		for i := 0; i < len(enc); i += 4096 {
			end := min(i+4096, len(enc))
			m := 1
			if end == len(enc) {
				m = 0
			}
			if i == 0 {
				fmt.Fprintf(&buf, "\x1b_Ga=T,f=32,s=%d,v=%d,i=1,p=1,C=1,q=2,m=%d;%s\x1b\\", w, h, m, enc[i:end])
			} else {
				fmt.Fprintf(&buf, "\x1b_Gm=%d;%s\x1b\\", m, enc[i:end])
			}
		}
		frames[f] = buf.Bytes()
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		br.Write(frames[i&1])
	}
}
