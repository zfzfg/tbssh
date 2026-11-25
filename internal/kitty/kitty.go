// Package kitty parses kitty graphics protocol commands
// (https://sw.kovidgoyal.net/kitty/graphics-protocol/): the control keys,
// chunked transmissions and the pixel payload.
package kitty

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"strconv"
)

// Command is one complete graphics command, with all chunks joined.
type Command struct {
	// Keys holds every control key that was sent, by its one-character name.
	Keys map[byte]string
	// Data is the decoded (base64, not decompressed) payload.
	Data []byte
}

// Char returns a one-character key, or def when absent.
func (c *Command) Char(k byte, def byte) byte {
	v, ok := c.Keys[k]
	if !ok || v == "" {
		return def
	}
	return v[0]
}

// Int returns a numeric key, or def when absent or malformed.
func (c *Command) Int(k byte, def int) int {
	v, ok := c.Keys[k]
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Has reports whether key k was sent.
func (c *Command) Has(k byte) bool {
	_, ok := c.Keys[k]
	return ok
}

// Action is the a= key, 't' when missing (transmit).
func (c *Command) Action() byte { return c.Char('a', 't') }

// Quiet is the q= key: 0 report everything, 1 only errors, 2 nothing.
func (c *Command) Quiet() int { return c.Int('q', 0) }

// ParseKeys splits "a=T,f=32,i=5" into a map.
func ParseKeys(s []byte) map[byte]string {
	keys := make(map[byte]string, 8)
	for len(s) > 0 {
		var field []byte
		if i := bytes.IndexByte(s, ','); i >= 0 {
			field, s = s[:i], s[i+1:]
		} else {
			field, s = s, nil
		}
		if len(field) >= 2 && field[1] == '=' {
			keys[field[0]] = string(field[2:])
		}
	}
	return keys
}

// Assembler joins chunked transmissions (m=1) into whole commands.
type Assembler struct {
	pending *Command
	b64     []byte
}

// Feed takes the payload of one APC string that starts with 'G' (without
// the 'G') and returns a complete command, or nil while chunks are still
// arriving. A malformed payload returns an error.
func (a *Assembler) Feed(payload []byte) (*Command, error) {
	ctrl, data := payload, []byte(nil)
	if i := bytes.IndexByte(payload, ';'); i >= 0 {
		ctrl, data = payload[:i], payload[i+1:]
	}
	keys := ParseKeys(ctrl)
	more := keys['m'] == "1"

	if a.pending != nil {
		// A continuation chunk. Per the protocol it only carries m (and
		// maybe q); any other action key means the previous transfer was
		// abandoned.
		if _, hasAction := keys['a']; hasAction && keys['a'] != "" {
			a.reset()
		} else {
			a.b64 = append(a.b64, data...)
			if q, ok := keys['q']; ok {
				a.pending.Keys['q'] = q
			}
			if more {
				return nil, nil
			}
			return a.finish()
		}
	}

	cmd := &Command{Keys: keys}
	if more {
		a.pending = cmd
		a.b64 = append(a.b64[:0], data...)
		return nil, nil
	}
	a.pending = cmd
	a.b64 = append(a.b64[:0], data...)
	return a.finish()
}

func (a *Assembler) reset() {
	a.pending = nil
	a.b64 = a.b64[:0]
}

func (a *Assembler) finish() (*Command, error) {
	cmd := a.pending
	raw := a.b64
	a.pending = nil
	var err error
	if len(raw) > 0 {
		cmd.Data, err = decodeBase64(raw)
	}
	a.b64 = a.b64[:0]
	if err != nil {
		return cmd, fmt.Errorf("EINVAL:bad base64 payload: %w", err)
	}
	return cmd, nil
}

// Pending reports whether a chunked transfer is in progress.
func (a *Assembler) Pending() bool { return a.pending != nil }

func decodeBase64(raw []byte) ([]byte, error) {
	clean := raw
	if bytes.ContainsAny(raw, "\n\r ") {
		clean = make([]byte, 0, len(raw))
		for _, b := range raw {
			if b != '\n' && b != '\r' && b != ' ' {
				clean = append(clean, b)
			}
		}
	}
	out := make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
	n, err := base64.StdEncoding.Decode(out, clean)
	if err == nil {
		return out[:n], nil
	}
	// Some senders omit padding.
	trimmed := bytes.TrimRight(clean, "=")
	out = make([]byte, base64.RawStdEncoding.DecodedLen(len(trimmed)))
	n, err2 := base64.RawStdEncoding.Decode(out, trimmed)
	if err2 != nil {
		return nil, err
	}
	return out[:n], nil
}

// ErrNotDirect is returned for transmission media other than direct (t=d).
var ErrNotDirect = errors.New("EBADF:tbssh only supports direct transmission")

// MaxPixels bounds a single image (about 64 megapixels).
const MaxPixels = 64 << 20

// DecodePixels turns the payload of a transmit or frame command into
// straight-alpha RGBA bytes with the given size. For PNG the size comes from
// the image itself.
func DecodePixels(cmd *Command) (w, h int, rgba []byte, err error) {
	switch cmd.Char('t', 'd') {
	case 'd':
	default:
		return 0, 0, nil, ErrNotDirect
	}
	data := cmd.Data
	if cmd.Char('o', 0) == 'z' {
		zr, zerr := zlib.NewReader(bytes.NewReader(data))
		if zerr != nil {
			return 0, 0, nil, fmt.Errorf("EINVAL:bad zlib data: %v", zerr)
		}
		limit := int64(MaxPixels*4 + 1)
		data, err = io.ReadAll(io.LimitReader(zr, limit))
		zr.Close()
		if err != nil {
			return 0, 0, nil, fmt.Errorf("EINVAL:bad zlib data: %v", err)
		}
	}
	format := cmd.Int('f', 32)
	switch format {
	case 32, 24:
		w, h = cmd.Int('s', 0), cmd.Int('v', 0)
		if w <= 0 || h <= 0 || w*h > MaxPixels {
			return 0, 0, nil, fmt.Errorf("EINVAL:bad image size %dx%d", w, h)
		}
		bpp := format / 8
		if len(data) < w*h*bpp {
			return 0, 0, nil, fmt.Errorf("ENODATA:need %d bytes, got %d", w*h*bpp, len(data))
		}
		if format == 32 {
			return w, h, data[:w*h*4], nil
		}
		out := make([]byte, w*h*4)
		for i, j := 0, 0; i < w*h*3; i, j = i+3, j+4 {
			out[j], out[j+1], out[j+2], out[j+3] = data[i], data[i+1], data[i+2], 255
		}
		return w, h, out, nil
	case 100:
		img, perr := png.Decode(bytes.NewReader(data))
		if perr != nil {
			return 0, 0, nil, fmt.Errorf("EBADPNG:%v", perr)
		}
		b := img.Bounds()
		if b.Dx()*b.Dy() > MaxPixels {
			return 0, 0, nil, errors.New("EINVAL:image too large")
		}
		nrgba, ok := img.(*image.NRGBA)
		if !ok || nrgba.Stride != b.Dx()*4 {
			nrgba = image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
			draw.Draw(nrgba, nrgba.Bounds(), img, b.Min, draw.Src)
		}
		return b.Dx(), b.Dy(), nrgba.Pix, nil
	}
	return 0, 0, nil, fmt.Errorf("EINVAL:unknown format %d", format)
}

// Reply builds the terminal's answer to a command: ESC _G i=..;msg ESC \.
// It returns nil when no answer is due (quiet mode, or no id given).
func Reply(cmd *Command, msg string) []byte {
	id := cmd.Int('i', 0)
	num := cmd.Int('I', 0)
	if id == 0 && num == 0 {
		return nil
	}
	ok := msg == "OK"
	switch q := cmd.Quiet(); {
	case q >= 2:
		return nil
	case q == 1 && ok:
		return nil
	}
	var b bytes.Buffer
	b.WriteString("\x1b_G")
	sep := ""
	if id != 0 {
		fmt.Fprintf(&b, "i=%d", id)
		sep = ","
	}
	if num != 0 {
		fmt.Fprintf(&b, "%sI=%d", sep, num)
		sep = ","
	}
	if p := cmd.Int('p', 0); p != 0 {
		fmt.Fprintf(&b, "%sp=%d", sep, p)
	}
	b.WriteByte(';')
	b.WriteString(msg)
	b.WriteString("\x1b\\")
	return b.Bytes()
}
