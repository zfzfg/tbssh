// Package vtstream splits a terminal output byte stream into tokens without
// changing a single byte: every token carries the exact bytes it was made of,
// so a consumer that forwards every token's Raw reproduces the input exactly.
//
// Only the sequences tbssh needs to look at are buffered whole (CSI, plain ESC
// sequences and APC strings). OSC, DCS, SOS and PM strings are streamed
// through as StringData tokens, so a large sixel image or clipboard payload
// coming from the remote side is never held in memory.
package vtstream

import (
	"bytes"
	"strconv"
)

// Kind tells what a Token is.
type Kind int

const (
	// Text is printable text and C0 controls outside of any sequence.
	Text Kind = iota
	// StringData is part of an OSC, DCS, SOS or PM string, including its
	// introducer and terminator. It is never printed by a terminal.
	StringData
	// Esc is a complete escape sequence that is not CSI or a string,
	// for example ESC 7 or ESC ( B.
	Esc
	// CSI is a complete control sequence (ESC [ ...).
	CSI
	// APC is a complete application program command string (ESC _ ... ESC \).
	APC
)

// Token is one piece of the stream. Raw and Payload alias the parser's
// buffers or the caller's input and are only valid during the callback.
type Token struct {
	Kind Kind
	Raw  []byte

	// CSI fields.
	Private byte   // '?', '>', '<' or '=' when the sequence starts with one
	Params  []byte // parameter bytes after Private, e.g. "1;2"
	Inter   []byte // intermediate bytes (0x20-0x2f)
	Final   byte   // final byte

	// APC fields: everything between ESC _ and the terminator.
	Payload []byte
}

// Param returns the i-th numeric CSI parameter, or def when it is missing or
// empty. Sub-parameters after ':' are ignored.
func (t *Token) Param(i, def int) int {
	idx := 0
	start := 0
	p := t.Params
	for j := 0; j <= len(p); j++ {
		if j == len(p) || p[j] == ';' {
			if idx == i {
				field := p[start:j]
				for k, b := range field {
					if b == ':' {
						field = field[:k]
						break
					}
				}
				if len(field) == 0 {
					return def
				}
				n, err := strconv.Atoi(string(field))
				if err != nil {
					return def
				}
				return n
			}
			idx++
			start = j + 1
		}
	}
	return def
}

// NumParams returns how many ';'-separated parameters the CSI has.
func (t *Token) NumParams() int {
	if len(t.Params) == 0 {
		return 0
	}
	n := 1
	for _, b := range t.Params {
		if b == ';' {
			n++
		}
	}
	return n
}

type state int

const (
	stGround state = iota
	stEsc
	stEscInter
	stCSI
	stString    // streamed OSC/DCS/SOS/PM
	stStringEsc // ESC seen inside a streamed string
	stAPC
	stAPCEsc
)

const (
	maxCSI = 4096
	// MaxAPC caps a buffered APC string. Kitty graphics chunks are small
	// (4096 bytes of base64), so anything far larger is not worth holding.
	MaxAPC = 64 << 20
)

// Parser is a resumable tokenizer. The zero value is ready to use.
type Parser struct {
	st      state
	buf     []byte // pending bytes of the current CSI/ESC/APC sequence
	isOSC   bool   // the streamed string is an OSC (BEL terminates it)
	overrun bool   // the APC grew past MaxAPC and is being discarded
}

// Feed tokenizes data and calls emit for each token, in order.
func (p *Parser) Feed(data []byte, emit func(Token)) {
	textStart := -1 // start of a pending Text run in data
	strStart := -1  // start of a pending StringData run in data
	flushText := func(end int) {
		if textStart >= 0 && end > textStart {
			emit(Token{Kind: Text, Raw: data[textStart:end]})
		}
		textStart = -1
	}
	flushStr := func(end int) {
		if strStart >= 0 && end > strStart {
			emit(Token{Kind: StringData, Raw: data[strStart:end]})
		}
		strStart = -1
	}

	for i := 0; i < len(data); i++ {
		b := data[i]
		switch p.st {
		case stGround:
			if b == 0x1b {
				flushText(i)
				p.buf = append(p.buf[:0], b)
				p.st = stEsc
				continue
			}
			if textStart < 0 {
				textStart = i
			}
			// Skip ahead to the next escape in one scan.
			if j := bytes.IndexByte(data[i+1:], 0x1b); j >= 0 {
				i += j
			} else {
				i = len(data) - 1
			}

		case stEsc:
			switch {
			case b == '[':
				p.buf = append(p.buf, b)
				p.st = stCSI
			case b == ']' || b == 'P' || b == 'X' || b == '^':
				p.isOSC = b == ']'
				p.buf = append(p.buf, b)
				emit(Token{Kind: StringData, Raw: p.buf})
				p.buf = p.buf[:0]
				p.st = stString
				strStart = i + 1
			case b == '_':
				p.buf = append(p.buf, b)
				p.overrun = false
				p.st = stAPC
			case b == 0x1b:
				// ESC ESC: the first one is a lone escape, emit it as text.
				emit(Token{Kind: Text, Raw: p.buf})
				p.buf = append(p.buf[:0], b)
			case b >= 0x20 && b <= 0x2f:
				p.buf = append(p.buf, b)
				p.st = stEscInter
			case b >= 0x30 && b <= 0x7e:
				p.buf = append(p.buf, b)
				p.emitEsc(emit)
			default:
				// A control inside an escape: give up on the escape and pass
				// both bytes on as text.
				p.buf = append(p.buf, b)
				emit(Token{Kind: Text, Raw: p.buf})
				p.buf = p.buf[:0]
				p.st = stGround
			}

		case stEscInter:
			switch {
			case b >= 0x20 && b <= 0x2f:
				p.buf = append(p.buf, b)
			case b >= 0x30 && b <= 0x7e:
				p.buf = append(p.buf, b)
				p.emitEsc(emit)
			case b == 0x1b:
				emit(Token{Kind: Text, Raw: p.buf})
				p.buf = append(p.buf[:0], b)
				p.st = stEsc
			default:
				p.buf = append(p.buf, b)
				emit(Token{Kind: Text, Raw: p.buf})
				p.buf = p.buf[:0]
				p.st = stGround
			}

		case stCSI:
			switch {
			case b >= 0x40 && b <= 0x7e:
				p.buf = append(p.buf, b)
				p.emitCSI(emit)
			case b == 0x1b:
				emit(Token{Kind: Text, Raw: p.buf})
				p.buf = append(p.buf[:0], b)
				p.st = stEsc
			case b == 0x18 || b == 0x1a: // CAN / SUB cancel the sequence
				p.buf = append(p.buf, b)
				emit(Token{Kind: Text, Raw: p.buf})
				p.buf = p.buf[:0]
				p.st = stGround
			default:
				p.buf = append(p.buf, b)
				if len(p.buf) > maxCSI {
					emit(Token{Kind: Text, Raw: p.buf})
					p.buf = p.buf[:0]
					p.st = stGround
				}
			}

		case stString:
			if b == 0x1b {
				flushStr(i)
				strStart = i
				p.st = stStringEsc
				continue
			}
			if b == 0x07 && p.isOSC {
				if strStart < 0 {
					strStart = i
				}
				flushStr(i + 1)
				p.st = stGround
				continue
			}
			if strStart < 0 {
				strStart = i
			}
			// Skip plain string bytes in one scan.
			if j := indexEscOrBel(data[i+1:]); j >= 0 {
				i += j
			} else {
				i = len(data) - 1
			}

		case stStringEsc:
			if b == '\\' {
				if strStart < 0 {
					// The ESC arrived in an earlier chunk.
					emit(Token{Kind: StringData, Raw: []byte{0x1b, '\\'}})
				} else {
					flushStr(i + 1)
				}
				p.st = stGround
				continue
			}
			// ESC followed by something else ends the string and starts a
			// new escape sequence. That ESC belongs to the new sequence, so it
			// is not emitted as string data.
			strStart = -1
			p.buf = append(p.buf[:0], 0x1b)
			p.st = stEsc
			i-- // reprocess b in stEsc
			continue

		case stAPC:
			if b == 0x1b {
				p.st = stAPCEsc
				continue
			}
			if b == 0x07 {
				// BEL is not a valid APC terminator for kitty, but some
				// programs use it. Accept it like most terminals do.
				p.buf = append(p.buf, b)
				p.emitAPC(emit, 1)
				continue
			}
			// Copy everything up to the next ESC or BEL at once.
			end := len(data)
			if j := indexEscOrBel(data[i:]); j >= 0 {
				end = i + j
			}
			p.appendAPC(data[i:end])
			i = end - 1

		case stAPCEsc:
			if b == '\\' {
				p.buf = append(p.buf, 0x1b, '\\')
				p.emitAPC(emit, 2)
				continue
			}
			// ESC inside an APC that is not ST: the APC is cancelled.
			emit(Token{Kind: Text, Raw: p.buf})
			p.buf = append(p.buf[:0], 0x1b)
			p.st = stEsc
			i--
			continue
		}
	}

	switch p.st {
	case stGround:
		flushText(len(data))
	case stString:
		flushStr(len(data))
	case stStringEsc:
		// The chunk ended right after an ESC inside a string. That ESC has
		// not been emitted; the next Feed emits it together with what follows.
	}
}

func (p *Parser) appendAPC(chunk []byte) {
	if p.overrun {
		return
	}
	if len(p.buf)+len(chunk) > MaxAPC {
		p.overrun = true
		return
	}
	p.buf = append(p.buf, chunk...)
}

// indexEscOrBel returns the index of the first ESC or BEL in s, or -1.
func indexEscOrBel(s []byte) int {
	e := bytes.IndexByte(s, 0x1b)
	if e < 0 {
		return bytes.IndexByte(s, 0x07)
	}
	if b := bytes.IndexByte(s[:e], 0x07); b >= 0 {
		return b
	}
	return e
}

func (p *Parser) emitEsc(emit func(Token)) {
	t := Token{Kind: Esc, Raw: p.buf, Final: p.buf[len(p.buf)-1]}
	if len(p.buf) > 2 {
		t.Inter = p.buf[1 : len(p.buf)-1]
	}
	emit(t)
	p.buf = p.buf[:0]
	p.st = stGround
}

func (p *Parser) emitCSI(emit func(Token)) {
	body := p.buf[2 : len(p.buf)-1]
	t := Token{Kind: CSI, Raw: p.buf, Final: p.buf[len(p.buf)-1]}
	if len(body) > 0 && body[0] >= 0x3c && body[0] <= 0x3f {
		t.Private = body[0]
		body = body[1:]
	}
	end := len(body)
	for end > 0 && body[end-1] >= 0x20 && body[end-1] <= 0x2f {
		end--
	}
	t.Params = body[:end]
	t.Inter = body[end:]
	emit(t)
	p.buf = p.buf[:0]
	p.st = stGround
}

func (p *Parser) emitAPC(emit func(Token), termLen int) {
	if p.overrun {
		p.buf = p.buf[:0]
		p.st = stGround
		p.overrun = false
		return
	}
	payload := p.buf[2 : len(p.buf)-termLen]
	emit(Token{Kind: APC, Raw: p.buf, Payload: payload})
	p.buf = p.buf[:0]
	p.st = stGround
}

// InSequence reports whether the parser is in the middle of a sequence.
func (p *Parser) InSequence() bool { return p.st != stGround }
