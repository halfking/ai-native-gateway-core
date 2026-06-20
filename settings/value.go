package settings

import "encoding/json"

// jsonMarshalAny is a thin wrapper so spec.go avoids importing encoding/json
// for just one call.
func jsonMarshalAny(v any) ([]byte, error) {
	return json.Marshal(v)
}
