package app

// escaper implements OpenSSH's escape character (default "~") recognized
// at the start of a line: "~." disconnects, "~?" prints help, "~r" redraws
// all images, "~~" sends a single "~".
type escaper struct {
	ch      byte
	enabled bool
	atStart bool
	pending bool
}

func newEscaper(spec string) *escaper {
	e := &escaper{ch: '~', enabled: true, atStart: true}
	switch {
	case spec == "" || spec == "~":
	case spec == "none":
		e.enabled = false
	case len(spec) == 2 && spec[0] == '^':
		e.ch = spec[1] & 0x1f
	case len(spec) == 1:
		e.ch = spec[0]
	}
	return e
}

const (
	escNone = iota
	escDisconnect
	escHelp
	escRedraw
)

// filter returns the bytes to send and an action found in the input.
func (e *escaper) filter(in []byte) (out []byte, action int) {
	if !e.enabled {
		return in, escNone
	}
	out = make([]byte, 0, len(in))
	for _, b := range in {
		if e.pending {
			e.pending = false
			switch b {
			case '.':
				return out, escDisconnect
			case '?':
				action = escHelp
				e.atStart = false
				continue
			case 'r':
				action = escRedraw
				e.atStart = false
				continue
			case e.ch:
				out = append(out, e.ch)
			default:
				out = append(out, e.ch, b)
			}
			e.atStart = b == '\r' || b == '\n'
			continue
		}
		if e.atStart && b == e.ch {
			e.pending = true
			continue
		}
		out = append(out, b)
		e.atStart = b == '\r' || b == '\n'
	}
	return out, action
}

const escapeHelp = "Supported escape sequences:\r\n" +
	" ~.   - terminate connection\r\n" +
	" ~?   - this message\r\n" +
	" ~r   - redraw all images\r\n" +
	" ~~   - send the escape character by typing it twice\r\n" +
	"(Note that escapes are only recognized immediately after newline.)\r\n"
