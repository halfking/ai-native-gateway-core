package sanitize

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp/syntax"
	"unicode"
)

// A match must contain at least one returned term. Unknown/nullable branches
// return nil and disable this optimization. This is only a negative prefilter;
// a possible match always goes through the complete original streaming regex.
func historyRequiredTerms(re *syntax.Regexp) [][]byte {
	switch re.Op {
	case syntax.OpLiteral:
		if len(re.Rune) == 0 {
			return nil
		}
		if re.Flags&syntax.FoldCase == 0 {
			return [][]byte{historyFoldASCII([]byte(string(re.Rune))[:min(128, len(string(re.Rune)))])}
		}
		// Prefer a long ASCII segment whose entire SimpleFold cycle stays
		// ASCII. In particular K and S have non-ASCII variants (K and ſ).
		var best, current []byte
		for _, ch := range re.Rune {
			safe := ch < 128
			for f := unicode.SimpleFold(ch); safe && f != ch; f = unicode.SimpleFold(f) {
				safe = f < 128
			}
			if safe {
				if len(current) < 128 {
					current = append(current, byte(ch))
				}
				if len(current) > len(best) {
					best = bytes.Clone(current)
				}
			} else {
				current = current[:0]
			}
		}
		if len(best) > 0 {
			return [][]byte{historyFoldASCII(best[:min(128, len(best))])}
		}
		// A single rune's complete fold cycle is also a necessary condition.
		// Keep its Unicode variants explicitly; ASCII normalization never
		// changes byte lengths or the overlap needed between input chunks.
		first := re.Rune[0]
		terms := [][]byte{historyFoldASCII([]byte(string(first)))}
		for ch := unicode.SimpleFold(first); ch != first; ch = unicode.SimpleFold(ch) {
			terms = append(terms, historyFoldASCII([]byte(string(ch))))
		}
		return terms
	case syntax.OpCharClass:
		var terms [][]byte
		for i := 0; i < len(re.Rune); i += 2 {
			if re.Rune[i+1]-re.Rune[i] > 128 || len(terms)+int(re.Rune[i+1]-re.Rune[i])+1 > 128 {
				return nil
			}
			for ch := re.Rune[i]; ch <= re.Rune[i+1]; ch++ {
				terms = append(terms, historyFoldASCII([]byte(string(ch))))
			}
		}
		return terms
	case syntax.OpCapture, syntax.OpPlus:
		return historyRequiredTerms(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min > 0 {
			return historyRequiredTerms(re.Sub[0])
		}
	case syntax.OpConcat:
		var best [][]byte
		quality := func(terms [][]byte) int {
			if len(terms) == 0 {
				return -1
			}
			n := 128
			for _, term := range terms {
				n = min(n, len(term))
			}
			return n*256 - len(terms)
		}
		for _, child := range re.Sub {
			if terms := historyRequiredTerms(child); quality(terms) > quality(best) {
				best = terms
			}
		}
		return best
	case syntax.OpAlternate:
		var terms [][]byte
		for _, child := range re.Sub {
			part := historyRequiredTerms(child)
			if len(part) == 0 || len(terms)+len(part) > 256 {
				return nil
			}
			terms = append(terms, part...)
		}
		return terms
	}
	return nil
}

func historyFoldASCII(p []byte) []byte {
	for i, ch := range p {
		if ch >= 'A' && ch <= 'Z' {
			p[i] = ch + ('a' - 'A')
		}
	}
	return p
}

func historyMayMatch(ctx context.Context, open func() io.Reader, terms [][]byte) (bool, error) {
	if len(terms) == 0 {
		return true, nil
	}
	reader := open()
	var block [32<<10 + 128]byte
	tail, empty := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, err := reader.Read(block[tail:])
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		if n > 0 {
			empty = 0
			historyFoldASCII(block[tail : tail+n])
			for _, term := range terms {
				if bytes.Contains(block[:tail+n], term) {
					return true, nil
				}
			}
			end := tail + n
			newTail := min(128, end)
			copy(block[:newTail], block[end-newTail:end])
			tail = newTail
		} else {
			empty++
			if empty >= 100 && err == nil {
				return false, io.ErrNoProgress
			}
		}
		if errors.Is(err, io.EOF) {
			return false, ctx.Err()
		}
	}
}
