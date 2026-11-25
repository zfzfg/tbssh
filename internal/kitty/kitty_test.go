package kitty

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func chunks(keys string, payload []byte, size int) [][]byte {
	enc := base64.StdEncoding.EncodeToString(payload)
	var out [][]byte
	for i := 0; i < len(enc) || i == 0; i += size {
		end := i + size
		if end > len(enc) {
			end = len(enc)
		}
		more := "0"
		if end < len(enc) {
			more = "1"
		}
		var head string
		if i == 0 {
			head = keys + ",m=" + more
		} else {
			head = "m=" + more
		}
		out = append(out, []byte(head+";"+enc[i:end]))
		if end == len(enc) {
			break
		}
	}
	return out
}

func zlibBytes(b []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func TestChunkedZlibRGBA(t *testing.T) {
	w, h := 7, 5
	rgba := make([]byte, w*h*4)
	for i := range rgba {
		rgba[i] = byte(i * 13)
	}
	var a Assembler
	var cmd *Command
	parts := chunks("a=T,f=32,o=z,s=7,v=5,t=d,i=2,p=1,C=1,z=0,q=2", zlibBytes(rgba), 8)
	if len(parts) < 3 {
		t.Fatalf("test needs several chunks, got %d", len(parts))
	}
	for i, p := range parts {
		c, err := a.Feed(p)
		if err != nil {
			t.Fatal(err)
		}
		if i < len(parts)-1 && c != nil {
			t.Fatalf("command completed early at chunk %d", i)
		}
		cmd = c
	}
	if cmd == nil || cmd.Action() != 'T' || cmd.Int('i', 0) != 2 || cmd.Int('C', 0) != 1 {
		t.Fatalf("bad command %+v", cmd)
	}
	gw, gh, px, err := DecodePixels(cmd)
	if err != nil || gw != w || gh != h || !bytes.Equal(px, rgba) {
		t.Fatalf("decode: %v %dx%d equal=%v", err, gw, gh, bytes.Equal(px, rgba))
	}
	if Reply(cmd, "OK") != nil {
		t.Fatal("q=2 must not reply")
	}
}

func TestRGBAndPNG(t *testing.T) {
	var a Assembler
	cmd, err := a.Feed([]byte("a=t,f=24,s=2,v=1,i=9;" + base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 4, 5, 6})))
	if err != nil {
		t.Fatal(err)
	}
	_, _, px, err := DecodePixels(cmd)
	if err != nil || !bytes.Equal(px, []byte{1, 2, 3, 255, 4, 5, 6, 255}) {
		t.Fatalf("rgb: %v %v", err, px)
	}

	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.NRGBA{10, 20, 30, 40})
	var buf bytes.Buffer
	png.Encode(&buf, img)
	cmd, _ = a.Feed([]byte("a=t,f=100,i=3;" + base64.StdEncoding.EncodeToString(buf.Bytes())))
	w, h, px, err := DecodePixels(cmd)
	if err != nil || w != 3 || h != 2 || !bytes.Equal(px[(1*3+1)*4:(1*3+1)*4+4], []byte{10, 20, 30, 40}) {
		t.Fatalf("png: %v %dx%d", err, w, h)
	}
}

func TestQueryReplies(t *testing.T) {
	var a Assembler
	cmd, _ := a.Feed([]byte("i=4207,a=q,t=d,f=24,s=1,v=1;AAAA"))
	if got := string(Reply(cmd, "OK")); got != "\x1b_Gi=4207;OK\x1b\\" {
		t.Fatalf("got %q", got)
	}
	cmd, _ = a.Feed([]byte("i=299,a=q,t=s,f=32,s=1,v=1;L3B4"))
	if _, _, _, err := DecodePixels(cmd); err != ErrNotDirect {
		t.Fatalf("shared memory must be refused, got %v", err)
	}
}
