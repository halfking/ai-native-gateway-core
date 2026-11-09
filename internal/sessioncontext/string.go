package sessioncontext

import (
	"bufio"
	"context"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

// stringExcerpt decodes a JSON string incrementally, including surrogate pairs.
// Matching blocks plus a fixed head/tail survive; arbitrary data is not claimed lossless.
func stringExcerpt(ctx context.Context, src io.Reader, query string, limit int) (string, error) {
	r := bufio.NewReaderSize(src, 64<<10)
	c, err := r.ReadByte()
	if err != nil || c != '"' {
		return "", largebody.ErrInvalidJSON
	}
	var first, hits strings.Builder
	tail := make([]byte, 0, 1024)
	block := make([]byte, 0, 4096)
	total := 0
	words := queryWords(query)
	flush := func() {
		if len(block) == 0 {
			return
		}
		total += len(block)
		if first.Len() < limit {
			first.WriteString(utf8Prefix(string(block), limit-first.Len()))
		}
		lower := strings.ToLower(string(block))
		if hits.Len() < limit/2 {
			for _, w := range words {
				if strings.Contains(lower, w) {
					hits.WriteString(sampleText(string(block), query, limit/2-hits.Len()))
					hits.WriteByte('\n')
					break
				}
			}
		}
		tail = append(tail, block...)
		if len(tail) > 1024 {
			copy(tail, tail[len(tail)-1024:])
			tail = tail[:1024]
		}
		block = block[:0]
	}
	hexRune := func() (rune, error) {
		var h [4]byte
		if _, e := io.ReadFull(r, h[:]); e != nil {
			return 0, e
		}
		v, e := strconv.ParseUint(string(h[:]), 16, 16)
		return rune(v), e
	}
	for {
		if len(block) == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		ch, size, e := r.ReadRune()
		if e != nil {
			return "", largebody.ErrInvalidJSON
		}
		if ch == utf8.RuneError && size == 1 {
			return "", largebody.ErrInvalidJSON
		}
		if ch == '"' {
			flush()
			break
		}
		if ch == '\\' {
			escape, e := r.ReadByte()
			if e != nil {
				return "", largebody.ErrInvalidJSON
			}
			switch escape {
			case '"', '\\', '/':
				ch = rune(escape)
			case 'b':
				ch = '\b'
			case 'f':
				ch = '\f'
			case 'n':
				ch = '\n'
			case 'r':
				ch = '\r'
			case 't':
				ch = '\t'
			case 'u':
				ch, e = hexRune()
				if e != nil {
					return "", largebody.ErrInvalidJSON
				}
				if ch >= 0xd800 && ch <= 0xdbff {
					a, e1 := r.ReadByte()
					b, e2 := r.ReadByte()
					if e1 != nil || e2 != nil || a != '\\' || b != 'u' {
						return "", largebody.ErrInvalidJSON
					}
					lo, e := hexRune()
					if e != nil || lo < 0xdc00 || lo > 0xdfff {
						return "", largebody.ErrInvalidJSON
					}
					ch = utf16.DecodeRune(ch, lo)
				} else if ch >= 0xdc00 && ch <= 0xdfff {
					return "", largebody.ErrInvalidJSON
				}
			default:
				return "", largebody.ErrInvalidJSON
			}
		} else if ch < 0x20 {
			return "", largebody.ErrInvalidJSON
		}
		block = utf8.AppendRune(block, ch)
		if len(block) >= 4092 {
			flush()
		}
	}
	if total <= limit {
		return first.String(), nil
	}
	remaining := max(0, limit-hits.Len()-32)
	return utf8Prefix(hits.String()+utf8Prefix(first.String(), remaining/2)+"\n[... omitted ...]\n"+utf8Suffix(string(tail), remaining-remaining/2), limit), nil
}
