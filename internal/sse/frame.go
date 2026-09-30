package sse

import "bytes"

type frameLine struct {
	content []byte
	ending  []byte
}

// ParseDataFrame joins SSE data fields with the protocol-mandated newline and
// returns a rewrite function that preserves non-data fields and line endings.
// Exactly one optional space after "data:" is excluded from the payload.
func ParseDataFrame(frame []byte) (payload []byte, hasData bool, rewrite func([]byte) []byte) {
	lines := splitFrameLines(frame)
	dataIndexes := make([]int, 0, 2)
	var firstPrefix []byte
	for index, line := range lines {
		if !bytes.HasPrefix(line.content, []byte("data:")) {
			continue
		}
		value := line.content[len("data:"):]
		prefix := line.content[:len("data:")]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
			prefix = line.content[:len("data: ")]
		}
		if len(dataIndexes) > 0 {
			payload = append(payload, '\n')
		}
		if len(dataIndexes) == 0 {
			firstPrefix = append([]byte(nil), prefix...)
		}
		dataIndexes = append(dataIndexes, index)
		payload = append(payload, value...)
	}
	if len(dataIndexes) == 0 {
		return nil, false, nil
	}
	firstData := dataIndexes[0]
	dataSet := make(map[int]struct{}, len(dataIndexes))
	for _, index := range dataIndexes {
		dataSet[index] = struct{}{}
	}
	return payload, true, func(rewritten []byte) []byte {
		var out bytes.Buffer
		for index, line := range lines {
			if index == firstData {
				out.Write(firstPrefix)
				out.Write(rewritten)
				out.Write(line.ending)
				continue
			}
			if _, isData := dataSet[index]; isData {
				continue
			}
			out.Write(line.content)
			out.Write(line.ending)
		}
		return out.Bytes()
	}
}

// FrameEnd returns the end of the first blank-line-delimited SSE frame. It
// recognizes LF, CRLF, and CR line endings, including mixed pairs. A trailing
// CR is held because a later write may complete it as CRLF.
func FrameEnd(frame []byte) (int, bool) {
	return frameEnd(frame, false)
}

// FrameEndAtEOF resolves a trailing CR as a lone-CR line ending.
func FrameEndAtEOF(frame []byte) (int, bool) {
	return frameEnd(frame, true)
}

func frameEnd(frame []byte, atEOF bool) (int, bool) {
	for index := 0; index < len(frame); {
		firstLength, ok := lineEndingLength(frame, index, atEOF)
		if !ok {
			index++
			continue
		}
		secondStart := index + firstLength
		secondLength, ok := lineEndingLength(frame, secondStart, atEOF)
		if ok {
			return secondStart + secondLength, true
		}
		index = secondStart
	}
	return 0, false
}

func lineEndingLength(frame []byte, index int, atEOF bool) (int, bool) {
	if index < 0 || index >= len(frame) {
		return 0, false
	}
	switch frame[index] {
	case '\n':
		return 1, true
	case '\r':
		if index+1 < len(frame) && frame[index+1] == '\n' {
			return 2, true
		}
		if index+1 == len(frame) && !atEOF {
			return 0, false
		}
		return 1, true
	default:
		return 0, false
	}
}

// DelimiterPrefixAtBoundary detects a blank-line delimiter that starts in the
// buffered suffix and finishes in the next write. It returns the prefix of p
// that belongs to the completed frame; zero bytes means the delimiter already
// ends at the pending buffer boundary (a trailing CR was resolved as lone CR).
func DelimiterPrefixAtBoundary(pending, p []byte) ([]byte, bool) {
	if len(pending) == 0 || len(p) == 0 {
		return nil, false
	}
	lineEndings := [][]byte{[]byte("\n"), []byte("\r"), []byte("\r\n")}
	bestLength := 0
	bestUsed := 0
	for _, first := range lineEndings {
		for _, second := range lineEndings {
			separator := append(append([]byte(nil), first...), second...)
			maxPrefix := len(separator)
			if maxPrefix > len(pending) {
				maxPrefix = len(pending)
			}
			for prefixLength := maxPrefix; prefixLength > 0; prefixLength-- {
				if !bytes.Equal(pending[len(pending)-prefixLength:], separator[:prefixLength]) {
					continue
				}
				needed := len(separator) - prefixLength
				if needed > len(p) || !bytes.Equal(p[:needed], separator[prefixLength:]) {
					continue
				}
				if needed == 0 && separator[len(separator)-1] == '\r' && p[0] == '\n' {
					continue
				}
				if len(separator) > bestLength {
					bestLength = len(separator)
					bestUsed = needed
				}
				break
			}
		}
	}
	if bestLength == 0 {
		return nil, false
	}
	return p[:bestUsed], true
}

func splitFrameLines(frame []byte) []frameLine {
	lines := make([]frameLine, 0, 8)
	for offset := 0; offset < len(frame); {
		end := offset
		for end < len(frame) && frame[end] != '\n' && frame[end] != '\r' {
			end++
		}
		line := frameLine{content: frame[offset:end]}
		if end < len(frame) {
			if frame[end] == '\r' && end+1 < len(frame) && frame[end+1] == '\n' {
				line.ending = frame[end : end+2]
				offset = end + 2
			} else {
				line.ending = frame[end : end+1]
				offset = end + 1
			}
		} else {
			offset = end
		}
		lines = append(lines, line)
	}
	return lines
}
