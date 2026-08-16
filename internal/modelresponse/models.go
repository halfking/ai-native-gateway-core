// Package modelresponse parses vendor model-list responses into model IDs.
package modelresponse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

var collectionKeys = []string{"data", "models", "items", "results", "result", "response"}
var modelIDKeys = []string{"id", "name", "model", "model_id", "model_name", "slug"}

// maxCollectDepth bounds collectModelIDs recursion. The parser only walks
// vendor model-list responses, where the realistic depth is 4-6 (wrapper →
// collection → model object). A cap of 64 is generous for any documented
// provider shape and still small enough to keep the goroutine stack safe
// against a hostile or pathological body that nests objects thousands deep
// (the previous implementation recursed without bound and would stack-overflow
// on such input, killing the worker goroutine).
const maxCollectDepth = 64

// ParseModelIDs accepts common OpenAI-compatible and vendor-wrapped model lists.
func ParseModelIDs(data []byte) ([]string, error) {
	var root any
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&root); err != nil {
		return nil, fmt.Errorf("parse models response failed: %w (context: body_bytes=%d)", err, len(data))
	}

	ids := make([]string, 0)
	seen := make(map[string]struct{})
	collectModelIDs(root, false, 0, &ids, seen)
	if len(ids) == 0 {
		return nil, fmt.Errorf("unrecognized models response format")
	}
	return ids, nil
}

func collectModelIDs(value any, inCollection bool, depth int, ids *[]string, seen map[string]struct{}) {
	if depth > maxCollectDepth {
		// Bound the recursion: the parser has walked deep enough that any
		// further model id is, by construction, unreachable through the
		// known collection keys. Stop descending instead of risking a
		// stack overflow on a hostile body.
		return
	}
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			collectModelIDs(item, true, depth+1, ids, seen)
		}
	case map[string]any:
		if inCollection {
			if id := modelIDFromObject(current); id != "" {
				appendModelID(id, ids, seen)
				return
			}
		}
		for _, key := range collectionKeys {
			if nested, ok := current[key]; ok {
				collectModelIDs(nested, key != "result" && key != "response", depth+1, ids, seen)
			}
		}
	case string:
		if inCollection {
			appendModelID(current, ids, seen)
		}
	}
}

func modelIDFromObject(model map[string]any) string {
	for _, key := range modelIDKeys {
		if value, ok := model[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func appendModelID(id string, ids *[]string, seen map[string]struct{}) {
	id = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(id), "models/"))
	if id == "" {
		return
	}
	key := strings.ToLower(id)
	if _, exists := seen[key]; exists {
		return
	}
	seen[key] = struct{}{}
	*ids = append(*ids, id)
}
