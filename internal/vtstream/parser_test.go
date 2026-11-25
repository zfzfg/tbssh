package vtstream

import (
	"bytes"
	"math/rand"
	"testing"
)

func collect(chunks ...[]byte) []Token {
	var p Parser
	var out []Token
	for _, c := range chunks {
		p.Feed(c, func(t Token) {
			cp := t
			cp.Raw = append([]byte(nil), t.Raw...)
			cp.Params = append([]byte(nil), t.Params...)
			cp.Inter = append([]byte(nil), t.Inter...)
			cp.Payload = append([]byte(nil), t.Payload...)
			out = append(out, cp)
		})
	}
	return out
}

func join(tokens []Token) []byte {
	var b bytes.Buffer
	for _, t := range tokens {
		b.Write(t.Raw)
	}
	return b.Bytes()
}

var sample = []byte("hello \x1b[1;31mred\x1b[0m\r\n" +
	"\x1b]0;title\x07\x1b]11;?\x1b\\" +
	"\x1b_Gi=31,a=q,t=d,f=24,s=1,v=1;AAAA\x1b\\\x1b[c" +
	"\x1bP0;1;0q\"1;1;2;6#1~~\x1b\\" +
	"\x1b7\x1b(B\x1b8\x1b[?1049h\x1b[>0q\x1b[16t" +
	"ümlaut 漢字 \x1b\x1b[A\x1b[?2026h\x1b_Ga=d,d=I,i=5,q=2\x1b\\\x1b[?2026l")

func TestRoundTripWhole(t *testing.T) {
	got := join(collect(sample))
	if !bytes.Equal(got, sample) {
		t.Fatalf("round trip differs:\n got %q\nwant %q", got, sample)
	}
}

func TestRoundTripRandomChunks(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 2000; iter++ {
		var chunks [][]byte
		rest := sample
		for len(rest) > 0 {
			n := 1 + rng.Intn(9)
			if n > len(rest) {
				n = len(rest)
			}
			chunks = append(chunks, rest[:n])
			rest = rest[n:]
		}
		toks := collect(chunks...)
		if got := join(toks); !bytes.Equal(got, sample) {
			t.Fatalf("iteration %d: round trip differs:\n got %q\nwant %q", iter, got, sample)
		}
		var apcs int
		for _, tk := range toks {
			if tk.Kind == APC {
				apcs++
			}
		}
		if apcs != 2 {
			t.Fatalf("iteration %d: %d APC tokens, want 2", iter, apcs)
		}
	}
}

func TestRandomBytesRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabet := []byte("\x1b[]_P\\?;0123456789Gahq\x07\r\nxyz")
	for iter := 0; iter < 3000; iter++ {
		data := make([]byte, rng.Intn(200))
		for i := range data {
			data[i] = alphabet[rng.Intn(len(alphabet))]
		}
		var chunks [][]byte
		rest := data
		for len(rest) > 0 {
			n := 1 + rng.Intn(7)
			if n > len(rest) {
				n = len(rest)
			}
			chunks = append(chunks, rest[:n])
			rest = rest[n:]
		}
		var p Parser
		var out bytes.Buffer
		for _, c := range chunks {
			p.Feed(c, func(tk Token) { out.Write(tk.Raw) })
		}
		// Whatever is still buffered inside an unfinished sequence is not
		// emitted yet; the emitted bytes must be a prefix of the input.
		if !bytes.HasPrefix(data, out.Bytes()) {
			t.Fatalf("iteration %d: output is not a prefix of input\n got %q\n in %q", iter, out.Bytes(), data)
		}
		if !p.InSequence() && out.Len() != len(data) {
			t.Fatalf("iteration %d: ground state but %d of %d bytes emitted", iter, out.Len(), len(data))
		}
	}
}

func TestCSIFields(t *testing.T) {
	toks := collect([]byte("\x1b[?1049;25h\x1b[>1u\x1b[6;20;10t\x1b[?1016$p\x1b[ q"))
	if len(toks) != 5 {
		t.Fatalf("got %d tokens", len(toks))
	}
	a := toks[0]
	if a.Kind != CSI || a.Private != '?' || a.Final != 'h' || a.Param(0, 0) != 1049 || a.Param(1, 0) != 25 || a.NumParams() != 2 {
		t.Fatalf("bad first token %+v", a)
	}
	if toks[1].Private != '>' || toks[1].Final != 'u' || toks[1].Param(0, 0) != 1 {
		t.Fatalf("bad kitty push %+v", toks[1])
	}
	if toks[2].Param(2, 0) != 10 || toks[2].Param(5, 99) != 99 {
		t.Fatalf("bad params %+v", toks[2])
	}
	if string(toks[3].Inter) != "$" || toks[3].Final != 'p' || toks[3].Param(0, 0) != 1016 {
		t.Fatalf("bad DECRQM %+v", toks[3])
	}
	if string(toks[4].Inter) != " " || toks[4].Final != 'q' {
		t.Fatalf("bad DECSCUSR %+v", toks[4])
	}
}

func TestAPCPayload(t *testing.T) {
	toks := collect([]byte("ab\x1b_Ga=T,i=2;QUJD\x1b\\cd"))
	if len(toks) != 3 || toks[1].Kind != APC || string(toks[1].Payload) != "Ga=T,i=2;QUJD" {
		t.Fatalf("unexpected tokens %+v", toks)
	}
}
