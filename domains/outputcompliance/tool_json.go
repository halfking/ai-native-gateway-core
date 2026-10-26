package outputcompliance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// decodeToolJSON rejects ambiguous duplicate keys, including escaped spellings,
// before a map can discard earlier values. No rejected key/value enters errors.
func decodeToolJSON(text string) (any, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 64 {
			return nil, fmt.Errorf("tool argument JSON depth exceeded")
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid tool argument JSON")
		}
		switch token {
		case json.Delim('{'):
			object := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok {
					return nil, fmt.Errorf("invalid tool argument key")
				}
				if _, exists := object[key]; exists {
					return nil, fmt.Errorf("duplicate tool argument key")
				}
				child, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				object[key] = child
			}
			if _, err := decoder.Token(); err != nil {
				return nil, fmt.Errorf("invalid tool argument object")
			}
			return object, nil
		case json.Delim('['):
			array := []any{}
			for decoder.More() {
				child, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				array = append(array, child)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, fmt.Errorf("invalid tool argument array")
			}
			return array, nil
		}
		return token, nil
	}
	root, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing tool argument JSON")
	}
	return root, nil
}
