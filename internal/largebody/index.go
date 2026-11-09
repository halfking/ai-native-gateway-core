package largebody

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	MaxDepth        = 64
	MaxObjectFields = 1024
	MaxArrayItems   = 100_000
	maxKeyBytes     = 512
	maxNodes        = 2_000_000
)

var (
	ErrInvalidJSON    = errors.New("largebody: invalid JSON object")
	ErrStructureLimit = errors.New("largebody: JSON structure exceeds processing limit")
)

// Document contains bounded byte coordinates, never message content. Arrays
// indexes only selected top-level fields. All positions are relative to the
// Reader supplied to Index, including when that Reader is a SectionReader.
type Document struct {
	Fields map[string]Span
	Arrays map[string][]Span
	Model  string
	Stream bool
}

func Index(ctx context.Context, r io.Reader) (*Document, error) { return indexObject(ctx, r, true) }

// IndexObject indexes any object, including a single message/content block.
// Unlike Index it does not impose request-envelope types on model or stream.
func IndexObject(ctx context.Context, r io.Reader) (*Document, error) {
	return indexObject(ctx, r, false)
}

func indexObject(ctx context.Context, r io.Reader, metadata bool) (*Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := jsonScanner{ctx: ctx, r: bufio.NewReaderSize(r, 64<<10), metadata: metadata, doc: &Document{Fields: make(map[string]Span), Arrays: make(map[string][]Span)}}
	if err := s.space(); err != nil {
		return nil, err
	}
	if c, err := s.peek(); err != nil || c != '{' {
		return nil, s.invalid()
	}
	if err := s.object(0, true); err != nil {
		return nil, err
	}
	if err := s.space(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if _, err := s.peek(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, s.invalid()
	}
	return s.doc, nil
}

type jsonScanner struct {
	ctx      context.Context
	r        *bufio.Reader
	pos      int64
	nodes    int
	metadata bool
	doc      *Document
	readErr  error
}

func (s *jsonScanner) invalid() error {
	if s.readErr != nil {
		return s.readErr
	}
	return fmt.Errorf("%w at byte %d", ErrInvalidJSON, s.pos)
}
func (s *jsonScanner) peek() (byte, error) {
	b, err := s.r.Peek(1)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			s.readErr = err
		}
		return 0, err
	}
	return b[0], nil
}
func (s *jsonScanner) next() (byte, error) {
	if s.pos&65535 == 0 {
		if err := s.ctx.Err(); err != nil {
			s.readErr = err
			return 0, err
		}
	}
	b, err := s.r.ReadByte()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return 0, s.invalid()
		}
		s.readErr = err
		return 0, err
	}
	s.pos++
	if s.pos > LimitBytes {
		return 0, ErrTooLarge
	}
	return b, nil
}
func (s *jsonScanner) want(want byte) error {
	b, err := s.next()
	if err != nil {
		return err
	}
	if b != want {
		return s.invalid()
	}
	return nil
}
func (s *jsonScanner) space() error {
	for {
		c, err := s.peek()
		if err != nil {
			return err
		}
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			return nil
		}
		if _, err = s.next(); err != nil {
			return err
		}
	}
}

func (s *jsonScanner) value(depth int) error {
	if depth > MaxDepth {
		return ErrStructureLimit
	}
	s.nodes++
	if s.nodes > maxNodes {
		return ErrStructureLimit
	}
	c, err := s.peek()
	if err != nil {
		return s.invalid()
	}
	switch c {
	case '{':
		return s.object(depth, false)
	case '[':
		_, err = s.array(depth, false)
		return err
	case '"':
		_, err = s.stringValue(false)
		return err
	case 't':
		return s.literal("true")
	case 'f':
		return s.literal("false")
	case 'n':
		return s.literal("null")
	default:
		if c == '-' || c >= '0' && c <= '9' {
			return s.number()
		}
		return s.invalid()
	}
}

func (s *jsonScanner) object(depth int, top bool) error {
	if depth > MaxDepth {
		return ErrStructureLimit
	}
	if err := s.want('{'); err != nil {
		return err
	}
	if err := s.space(); err != nil {
		return s.invalid()
	}
	if c, _ := s.peek(); c == '}' {
		return s.want('}')
	}
	// Hashes bound duplicate-key state independently of arbitrary key length.
	seen := make(map[[32]byte]struct{})
	for fields := 0; ; fields++ {
		if fields >= MaxObjectFields {
			return ErrStructureLimit
		}
		key, err := s.stringValue(true)
		if err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(key))
		if _, ok := seen[hash]; ok {
			return s.invalid()
		}
		seen[hash] = struct{}{}
		if err = s.space(); err != nil {
			return s.invalid()
		}
		if err = s.want(':'); err != nil {
			return err
		}
		if err = s.space(); err != nil {
			return s.invalid()
		}
		start := s.pos
		if top && isIndexedArray(key) {
			if c, _ := s.peek(); c == '[' {
				var spans []Span
				spans, err = s.array(depth+1, true)
				s.doc.Arrays[key] = spans
			} else {
				err = s.value(depth + 1)
			}
		} else if top && s.metadata && (key == "model" || key == "stream") {
			err = s.envelopeMetadata(key)
		} else {
			err = s.value(depth + 1)
		}
		if err != nil {
			return err
		}
		if top {
			s.doc.Fields[key] = Span{Offset: start, Length: s.pos - start}
		}
		if err = s.space(); err != nil {
			return s.invalid()
		}
		c, _ := s.peek()
		if c == '}' {
			return s.want('}')
		}
		if c != ',' {
			return s.invalid()
		}
		if err = s.want(','); err != nil {
			return err
		}
		if err = s.space(); err != nil {
			return s.invalid()
		}
	}
}

func isIndexedArray(key string) bool {
	switch key {
	case "messages", "input", "contents", "content", "parts":
		return true
	}
	return false
}

func (s *jsonScanner) array(depth int, index bool) ([]Span, error) {
	if depth > MaxDepth {
		return nil, ErrStructureLimit
	}
	if err := s.want('['); err != nil {
		return nil, err
	}
	if err := s.space(); err != nil {
		return nil, s.invalid()
	}
	var spans []Span
	if c, _ := s.peek(); c == ']' {
		return spans, s.want(']')
	}
	for items := 0; ; items++ {
		if items >= MaxArrayItems {
			return nil, ErrStructureLimit
		}
		start := s.pos
		if err := s.value(depth + 1); err != nil {
			return nil, err
		}
		if index {
			spans = append(spans, Span{Offset: start, Length: s.pos - start})
		}
		if err := s.space(); err != nil {
			return nil, s.invalid()
		}
		c, _ := s.peek()
		if c == ']' {
			return spans, s.want(']')
		}
		if c != ',' {
			return nil, s.invalid()
		}
		if err := s.want(','); err != nil {
			return nil, err
		}
		if err := s.space(); err != nil {
			return nil, s.invalid()
		}
	}
}

func (s *jsonScanner) envelopeMetadata(key string) error {
	c, err := s.peek()
	if err != nil {
		return s.invalid()
	}
	if c == 'n' {
		return s.literal("null")
	}
	if key == "model" {
		if c != '"' {
			return s.invalid()
		}
		s.doc.Model, err = s.stringValue(true)
		return err
	}
	switch c {
	case 't':
		s.doc.Stream = true
		return s.literal("true")
	case 'f':
		s.doc.Stream = false
		return s.literal("false")
	default:
		return s.invalid()
	}
}

func (s *jsonScanner) literal(text string) error {
	for i := range len(text) {
		if err := s.want(text[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *jsonScanner) number() error {
	c, _ := s.peek()
	if c == '-' {
		if _, err := s.next(); err != nil {
			return err
		}
		c, _ = s.peek()
	}
	if c == '0' {
		if _, err := s.next(); err != nil {
			return err
		}
		if c, _ = s.peek(); isDigit(c) {
			return s.invalid()
		}
	} else {
		if c < '1' || c > '9' {
			return s.invalid()
		}
		if err := s.digits(); err != nil {
			return err
		}
	}
	if c, _ = s.peek(); c == '.' {
		if _, err := s.next(); err != nil {
			return err
		}
		if c, _ = s.peek(); !isDigit(c) {
			return s.invalid()
		}
		if err := s.digits(); err != nil {
			return err
		}
	}
	if c, _ = s.peek(); c == 'e' || c == 'E' {
		if _, err := s.next(); err != nil {
			return err
		}
		c, _ = s.peek()
		if c == '+' || c == '-' {
			if _, err := s.next(); err != nil {
				return err
			}
		}
		if c, _ = s.peek(); !isDigit(c) {
			return s.invalid()
		}
		if err := s.digits(); err != nil {
			return err
		}
	}
	return nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func (s *jsonScanner) digits() error {
	for {
		c, err := s.peek()
		if err != nil || !isDigit(c) {
			return nil
		}
		if _, err = s.next(); err != nil {
			return err
		}
	}
}

// stringValue validates arbitrarily large string values in constant space.
// Only object keys and model metadata are decoded, with a strict small cap.
func (s *jsonScanner) stringValue(capture bool) (string, error) {
	if err := s.want('"'); err != nil {
		return "", err
	}
	var out strings.Builder
	for {
		c, err := s.next()
		if err != nil {
			return "", err
		}
		if c == '"' {
			return out.String(), nil
		}
		if c < 0x20 {
			return "", s.invalid()
		}
		var value rune
		switch {
		case c == '\\':
			e, err := s.next()
			if err != nil {
				return "", err
			}
			switch e {
			case '"', '\\', '/':
				value = rune(e)
			case 'b':
				value = '\b'
			case 'f':
				value = '\f'
			case 'n':
				value = '\n'
			case 'r':
				value = '\r'
			case 't':
				value = '\t'
			case 'u':
				value, err = s.unicodeEscape()
				if err != nil {
					return "", err
				}
			default:
				return "", s.invalid()
			}
		case c >= utf8.RuneSelf:
			var encoded [4]byte
			encoded[0] = c
			n := 0
			switch {
			case c >= 0xc2 && c <= 0xdf:
				n = 2
			case c >= 0xe0 && c <= 0xef:
				n = 3
			case c >= 0xf0 && c <= 0xf4:
				n = 4
			default:
				return "", s.invalid()
			}
			for i := 1; i < n; i++ {
				encoded[i], err = s.next()
				if err != nil {
					return "", err
				}
			}
			var size int
			value, size = utf8.DecodeRune(encoded[:n])
			if size != n {
				return "", s.invalid()
			}
		default:
			value = rune(c)
		}
		if capture {
			if out.Len()+utf8.RuneLen(value) > maxKeyBytes {
				return "", ErrStructureLimit
			}
			out.WriteRune(value)
		}
	}
}

func (s *jsonScanner) hexRune() (rune, error) {
	var value rune
	for range 4 {
		c, err := s.next()
		if err != nil {
			return 0, err
		}
		var n byte
		switch {
		case c >= '0' && c <= '9':
			n = c - '0'
		case c >= 'a' && c <= 'f':
			n = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			n = c - 'A' + 10
		default:
			return 0, s.invalid()
		}
		value = value<<4 | rune(n)
	}
	return value, nil
}

func (s *jsonScanner) unicodeEscape() (rune, error) {
	r, err := s.hexRune()
	if err != nil {
		return 0, err
	}
	if r >= 0xdc00 && r <= 0xdfff {
		return 0, s.invalid()
	}
	if r < 0xd800 || r > 0xdbff {
		return r, nil
	}
	if err = s.want('\\'); err != nil {
		return 0, err
	}
	if err = s.want('u'); err != nil {
		return 0, err
	}
	low, err := s.hexRune()
	if err != nil {
		return 0, err
	}
	if low < 0xdc00 || low > 0xdfff {
		return 0, s.invalid()
	}
	return utf16.DecodeRune(r, low), nil
}
