//go:build windows

package app

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"regexp"
	"time"

	"tbssh/internal/bridge"
	"tbssh/internal/console"
	"tbssh/internal/sshclient"
)

// doctor prints what tbssh learns about the local terminal.
func doctor() int {
	fmt.Printf("tbssh %s\n", Version)
	if p, err := sshclient.FindSSH(); err == nil {
		fmt.Printf("  OpenSSH client:   %s\n", p)
	} else {
		fmt.Printf("  OpenSSH client:   NOT FOUND (%v)\n", err)
	}
	if os.Getenv("WT_SESSION") != "" {
		fmt.Printf("  Terminal:         Windows Terminal (WT_SESSION set)\n")
	} else {
		fmt.Printf("  Terminal:         not Windows Terminal (WT_SESSION missing)\n")
	}
	con, err := console.Open()
	if err != nil {
		fmt.Printf("  Console:          %v\n", err)
		return 1
	}
	if err := con.MakeRaw(); err != nil {
		fmt.Printf("  Console:          %v\n", err)
		return 1
	}
	caps := con.Probe(1500 * time.Millisecond)
	cell := con.Ask("\x1b[16t\x1b[14t\x1b[c", 1500*time.Millisecond, func(b []byte) bool {
		return regexp.MustCompile(`\x1b\[\?[0-9;]*c`).Match(b)
	})
	cols, rows := con.Size()
	con.Restore()

	fmt.Printf("  Device attributes: %q\n", caps.DA1)
	if caps.Sixel {
		fmt.Printf("  Sixel:            yes\n")
	} else {
		fmt.Printf("  Sixel:            NO - Windows Terminal 1.22 or newer is needed\n")
	}
	if caps.HasBG {
		fmt.Printf("  Background:       #%02x%02x%02x\n", caps.Background[0], caps.Background[1], caps.Background[2])
	}
	if m := regexp.MustCompile(`\x1b\[6;(\d+);(\d+)t`).FindSubmatch(cell); m != nil {
		fmt.Printf("  Cell size (16t):  %sx%s px reported by the terminal\n", m[2], m[1])
	} else {
		fmt.Printf("  Cell size (16t):  no answer\n")
	}
	if m := regexp.MustCompile(`\x1b\[4;(\d+);(\d+)t`).FindSubmatch(cell); m != nil {
		fmt.Printf("  Text area (14t):  %sx%s px reported by the terminal\n", m[2], m[1])
	}
	fmt.Printf("  Window:           %dx%d cells -> frames of %dx%d px for the remote side\n", cols, rows, cols*10, rows*20)
	fmt.Printf("  Graphics mode:    %s (TBSSH_GRAPHICS)\n", graphicsMode())
	fmt.Println()
	if caps.Sixel {
		fmt.Println("Everything needed is there. Run 'tbssh --demo' for a visual test.")
		return 0
	}
	fmt.Println("Without sixel support tbssh works as a plain ssh client; terminal-browser then uses its text mode.")
	return 1
}

// kittyTransmit encodes RGBA pixels the way terminal-browser does: zlib,
// base64, 4096 byte chunks.
func kittyTransmit(keys string, rgba []byte) []byte {
	var z bytes.Buffer
	w, _ := zlib.NewWriterLevel(&z, 1)
	w.Write(rgba)
	w.Close()
	enc := base64.StdEncoding.EncodeToString(z.Bytes())
	var b bytes.Buffer
	for i := 0; i < len(enc) || i == 0; i += 4096 {
		end := min(i+4096, len(enc))
		more := 0
		if end < len(enc) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(&b, "\x1b_G%s,m=%d;%s\x1b\\", keys, more, enc[i:end])
		} else {
			fmt.Fprintf(&b, "\x1b_Gm=%d;%s\x1b\\", more, enc[i:end])
		}
		if end == len(enc) {
			break
		}
	}
	return b.Bytes()
}

// demo draws an animated scene through the bridge, exactly like a remote
// terminal-browser would, to check the local half without a server.
func demo() int {
	con, err := console.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tbssh: %v\n", err)
		return 1
	}
	if err := con.MakeRaw(); err != nil {
		fmt.Fprintf(os.Stderr, "tbssh: %v\n", err)
		return 1
	}
	defer con.Restore()
	caps := con.Probe(time.Second)
	if !caps.Sixel && graphicsMode() != "on" {
		con.Restore()
		fmt.Fprintln(os.Stderr, "tbssh: this terminal does not report sixel support (see tbssh --doctor)")
		return 1
	}
	cols, rows := con.Size()
	br := bridge.New(os.Stdout, nil, bridge.Options{Cols: cols, Rows: rows, Graphics: true, Background: caps.Background, MaxFPS: 60})
	W, H := cols*10, rows*20

	keys := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, 64)
		con.Read(buf)
		keys <- struct{}{}
	}()

	br.Write([]byte("\x1b[?1049h\x1b[?25l"))
	base := make([]byte, W*H*4)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			i := (y*W + x) * 4
			base[i] = byte(x * 255 / max(W-1, 1))
			base[i+1] = byte(y * 255 / max(H-1, 1))
			base[i+2] = 160
			base[i+3] = 255
			// A white grid every two cells helps to judge alignment.
			if x%20 == 0 || y%40 == 0 {
				base[i], base[i+1], base[i+2] = 255, 255, 255
			}
		}
	}
	frame := append([]byte("\x1b[?2026h\x1b[H"), kittyTransmit(fmt.Sprintf("a=T,f=32,o=z,s=%d,v=%d,t=d,i=1,p=1,C=1,q=2", W, H), base)...)
	frame = append(frame, "\x1b[?2026l"...)
	br.Write(frame)

	const size = 48
	square := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			i := (y*size + x) * 4
			dx, dy := float64(x-size/2), float64(y-size/2)
			if dx*dx+dy*dy <= float64(size*size/4) {
				square[i], square[i+1], square[i+2], square[i+3] = 255, 210, 0, 255
			}
		}
	}
	start := time.Now()
	frames := 0
	tick := time.NewTicker(time.Second / 60)
	defer tick.Stop()
loop:
	for time.Since(start) < 20*time.Second {
		select {
		case <-keys:
			break loop
		case <-tick.C:
		}
		t := time.Since(start).Seconds()
		px := int((math.Sin(t*1.3)*0.5 + 0.5) * float64(W-size))
		py := int((math.Cos(t*0.9)*0.5 + 0.5) * float64(H-size))
		col, row := px/10, py/20
		var f bytes.Buffer
		fmt.Fprintf(&f, "\x1b[?2026h\x1b[%d;%dH", row+1, col+1)
		f.Write(kittyTransmit(fmt.Sprintf("a=T,f=32,o=z,s=%d,v=%d,t=d,i=2,p=1,C=1,z=2,X=%d,Y=%d,q=2", size, size, px%10, py%20), square))
		f.WriteString("\x1b[?2026l")
		br.Write(f.Bytes())
		frames++
	}
	elapsed := time.Since(start).Seconds()
	br.Write([]byte("\x1b_Ga=d,d=A,q=2\x1b\\\x1b[?25h\x1b[?1049l"))
	br.Close()
	con.Restore()
	stats := br.Stats
	fmt.Printf("tbssh demo: %d frames in %.1fs (%.0f fps), %.1f MB sixel, %.1f KB per frame\n",
		frames, elapsed, float64(frames)/elapsed, float64(stats.SixelBytes)/1e6, float64(stats.SixelBytes)/1e3/float64(max(stats.Frames, 1)))
	fmt.Println("You should have seen a color gradient with a white grid and a yellow ball moving over it.")
	return 0
}

// replay plays a recording of remote output through the bridge.
func replay(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tbssh: %v\n", err)
		return 1
	}
	con, err := console.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tbssh: %v\n", err)
		return 1
	}
	if err := con.MakeRaw(); err != nil {
		fmt.Fprintf(os.Stderr, "tbssh: %v\n", err)
		return 1
	}
	defer con.Restore()
	caps := con.Probe(time.Second)
	cols, rows := con.Size()
	br := bridge.New(os.Stdout, nil, bridge.Options{Cols: cols, Rows: rows, Graphics: true, Background: caps.Background, MaxFPS: 60})
	start := time.Now()
	for len(data) > 0 {
		n := min(len(data), 64<<10)
		br.Write(data[:n])
		data = data[n:]
	}
	took := time.Since(start)
	buf := make([]byte, 64)
	con.Read(buf)
	br.Close()
	con.Restore()
	fmt.Printf("replayed in %v, stats %+v\n", took, br.Stats)
	return 0
}
