package sessioncontext

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

// HistoryGuard checks a complete historical message before any lossy sampling.
// The factory provides independently reopenable, decoded textual streams.
// true means safe to excerpt; false omits the entire historical message.
type HistoryGuard func(context.Context, func() io.Reader) (bool, error)

type historyGuardKey struct{}

func WithHistoryGuard(ctx context.Context, guard HistoryGuard) context.Context {
	return context.WithValue(ctx, historyGuardKey{}, guard)
}

func HasHistoryGuard(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	guard, ok := ctx.Value(historyGuardKey{}).(HistoryGuard)
	return ok && guard != nil
}

type historyReadFailure struct {
	mu  sync.Mutex
	err error
}

type historyErrorReader struct {
	io.Reader
	failure *historyReadFailure
}

func (r historyErrorReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil && err != io.EOF {
		r.failure.mu.Lock()
		if r.failure.err == nil {
			r.failure.err = err
		}
		r.failure.mu.Unlock()
	}
	return n, err
}

func checkHistoryGuard(ctx context.Context, source func() io.Reader) (bool, error) {
	guard, _ := ctx.Value(historyGuardKey{}).(HistoryGuard)
	if guard == nil {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	failure := &historyReadFailure{}
	safe, err := guard(ctx, func() io.Reader {
		return historyErrorReader{Reader: newHistoryText(ctx, source), failure: failure}
	})
	if err != nil {
		return false, err
	}
	failure.mu.Lock()
	readErr := failure.err
	failure.mu.Unlock()
	if readErr != nil {
		return false, readErr // regexp.MatchReader itself suppresses reader errors.
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return safe, nil
}

// Two complete textual views preserve both actual outer-string semantics and
// nested serialized-JSON escapes. Expanding the latter is conservative and may
// identify more secrets; it must never hide a match from the ordinary view.
// Quotes, keys and punctuation remain visible. This projection is NOT JSON for
// forwarding. Each view owns only an 8 KiB buffer; no string is materialized.
func newHistoryText(ctx context.Context, source func() io.Reader) io.Reader {
	newView := func(expand bool) *historyTextReader {
		r := &historyTextReader{ctx: ctx, raw: bufio.NewReaderSize(source(), 8<<10), expand: expand}
		r.nested.next = r.contentRune
		return r
	}
	return io.MultiReader(newView(false), strings.NewReader("\n"), newView(true))
}

var (
	errHistoryJSON   = errors.New("invalid JSON string in historical guard stream")
	errHistoryEscape = errors.New("invalid nested historical escape")
)

type historyTextReader struct {
	ctx             context.Context
	raw             *bufio.Reader
	expand          bool
	inString, ended bool
	reads           uint64
	nested          historyNestedEscapes
	pending         [utf8.UTFMax]byte
	start, end      int
	err             error
}

func (r *historyTextReader) rawRune() (rune, error) {
	r.reads++
	if r.reads&255 == 1 {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	ch, size, err := r.raw.ReadRune()
	if err == nil && ch == utf8.RuneError && size == 1 {
		return 0, errHistoryJSON
	}
	return ch, err
}

func (r *historyTextReader) contentRune() (rune, error) {
	if r.ended {
		return 0, io.EOF
	}
	ch, err := r.rawRune()
	if err == io.EOF {
		return 0, io.ErrUnexpectedEOF
	}
	if err != nil {
		return 0, err
	}
	if ch == '"' {
		r.ended = true
		return 0, io.EOF
	}
	if ch < 0x20 {
		return 0, errHistoryJSON
	}
	if ch == '\\' {
		var consumed [11]rune
		ch, _, err = historyEscape(r.rawRune, &consumed)
		if err == errHistoryEscape || err == io.EOF {
			err = errHistoryJSON
		}
	}
	return ch, err
}

func (r *historyTextReader) nextRune() (rune, error) {
	if r.inString {
		var ch rune
		var err error
		if r.expand {
			ch, err = r.nested.readRune()
		} else {
			ch, err = r.contentRune()
		}
		if err == io.EOF {
			r.inString = false
			return '"', nil
		}
		return ch, err
	}
	ch, err := r.rawRune()
	if err == nil && ch == '"' {
		r.inString, r.ended = true, false
		r.nested.pos, r.nested.size = 0, 0
	}
	return ch, err
}

func (r *historyTextReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n := copy(p, r.pending[r.start:r.end])
	r.start += n
	for n < len(p) {
		ch, err := r.nextRune()
		if err != nil {
			r.err = err
			return n, err
		}
		if ch < utf8.RuneSelf {
			p[n] = byte(ch)
			n++
			continue
		}
		encoded := utf8.AppendRune(r.pending[:0], ch)
		copied := copy(p[n:], encoded)
		r.start, r.end = copied, len(encoded)
		n += copied
	}
	return n, nil
}

type historyNestedEscapes struct {
	next      func() (rune, error)
	pending   [11]rune
	pos, size int
}

func (r *historyNestedEscapes) take() (rune, error) {
	if r.pos < r.size {
		ch := r.pending[r.pos]
		r.pos++
		return ch, nil
	}
	return r.next()
}

func (r *historyNestedEscapes) readRune() (rune, error) {
	ch, err := r.take()
	if err != nil || ch != '\\' {
		return ch, err
	}
	for {
		var consumed [11]rune
		ch, size, err := historyEscape(r.take, &consumed)
		if err == errHistoryEscape || err == io.EOF {
			// Literal, non-JSON backslashes remain visible to the guard.
			remaining := copy(r.pending[size:], r.pending[r.pos:r.size])
			copy(r.pending[:], consumed[:size])
			r.pos, r.size = 0, size+remaining
			return '\\', nil
		}
		if err != nil || ch != '\\' {
			return ch, err
		}
	}
}

func historyEscape(next func() (rune, error), consumed *[11]rune) (rune, int, error) {
	size := 0
	take := func() (rune, error) {
		ch, err := next()
		if err == nil {
			consumed[size] = ch
			size++
		}
		return ch, err
	}
	ch, err := take()
	if err != nil {
		return 0, size, err
	}
	switch ch {
	case '"', '\\', '/':
		return ch, size, nil
	case 'b':
		return '\b', size, nil
	case 'f':
		return '\f', size, nil
	case 'n':
		return '\n', size, nil
	case 'r':
		return '\r', size, nil
	case 't':
		return '\t', size, nil
	case 'u':
		hex := func() (rune, error) {
			var value rune
			for i := 0; i < 4; i++ {
				c, err := take()
				if err != nil {
					return 0, err
				}
				n := strings.IndexRune("0123456789abcdef", c)
				if n < 0 {
					n = strings.IndexRune("0123456789ABCDEF", c)
				}
				if n < 0 {
					return 0, errHistoryEscape
				}
				value = value*16 + rune(n)
			}
			return value, nil
		}
		value, err := hex()
		if err != nil {
			return 0, size, err
		}
		if value >= 0xd800 && value <= 0xdbff {
			slash, e1 := take()
			u, e2 := take()
			if e1 != nil {
				return 0, size, e1
			}
			if e2 != nil {
				return 0, size, e2
			}
			if slash != '\\' || u != 'u' {
				return 0, size, errHistoryEscape
			}
			low, err := hex()
			if err != nil {
				return 0, size, err
			}
			if low < 0xdc00 || low > 0xdfff {
				return 0, size, errHistoryEscape
			}
			return utf16.DecodeRune(value, low), size, nil
		}
		if value >= 0xdc00 && value <= 0xdfff {
			return 0, size, errHistoryEscape
		}
		return value, size, nil
	default:
		return 0, size, errHistoryEscape
	}
}
