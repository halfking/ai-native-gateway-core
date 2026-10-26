package credentialtext

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

var jsonStringToken = regexp.MustCompile(`"(?:\\.|[^"\\])*"`)

// Embedded tool JSON escapes the sed quotes. Check decoded string tokens, but
// map only proven masking spans back to the original bytes. Decode once so
// nested/untrusted encodings cannot trigger recursive work or broad exemptions.
func maskJSONRedaction(text, view string, spans [][2]int) (string, [][2]int) {
	if !strings.Contains(text, "sed") {
		return view, spans
	}
	var result []byte
	for _, token := range jsonStringToken.FindAllStringIndex(text, -1) {
		raw := text[token[0]:token[1]]
		if !strings.Contains(raw, "sed") || !utf8.ValidString(raw) {
			continue
		}
		var decoded string
		if json.Unmarshal([]byte(raw), &decoded) != nil {
			continue
		}
		_, decodedSpans := detectionView(decoded)
		if len(decodedSpans) == 0 {
			continue
		}
		offsets := jsonStringOffsets(raw)
		if result == nil {
			result = []byte(view)
		}
		for _, span := range decodedSpans {
			start, end := token[0]+offsets[span[0]], token[0]+offsets[span[1]]
			blank(result, start, end)
			spans = append(spans, [2]int{start, end})
		}
	}
	if result != nil {
		view = string(result)
	}
	return view, spans
}

// raw is a validated JSON string. Each boundary maps a decoded byte offset to
// its source position, including UTF-8 and JSON's UTF-16 surrogate pairs.
func jsonStringOffsets(raw string) []int {
	offsets := make([]int, 0, len(raw))
	for i := 1; i < len(raw)-1; {
		end, width := i+1, 1
		if raw[i] == '\\' {
			end = i + 2
			if raw[i+1] == 'u' {
				end = i + 6
				if end+6 <= len(raw)-1 && strings.EqualFold(raw[i+2:i+3], "d") && strings.ContainsAny(raw[i+3:i+4], "89aAbB") && strings.EqualFold(raw[end:end+3], `\ud`) && strings.ContainsAny(raw[end+3:end+4], "cCdDeEfF") {
					end += 6
				}
			}
			var part string
			_ = json.Unmarshal([]byte(`"`+raw[i:end]+`"`), &part)
			width = len(part)
		}
		for n := 0; n < width; n++ {
			offsets = append(offsets, i)
		}
		i = end
	}
	return append(offsets, len(raw)-1)
}
