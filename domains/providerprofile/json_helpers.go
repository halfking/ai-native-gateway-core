package providerprofile

import "encoding/json"

// marshalJSON 将对象序列化为JSON字节
func marshalJSON(v interface{}) ([]byte, error) {
	if v == nil {
		return []byte("null"), nil
	}
	// json.Marshal produces "null" for nil maps/slices, but our JSONB columns
	// expect {}. Convert nil maps/slices to empty non-nil values before marshal.
	switch x := v.(type) {
	case map[string]interface{}:
		if x == nil {
			return []byte("{}"), nil
		}
	case map[string]int:
		if x == nil {
			return []byte("{}"), nil
		}
	case map[TimeSlot]float64:
		if x == nil {
			return []byte("{}"), nil
		}
	case []interface{}:
		if x == nil {
			return []byte("[]"), nil
		}
	}
	return json.Marshal(v)
}

// unmarshalJSON 将JSON字节反序列化为对象
func unmarshalJSON(data []byte, v interface{}) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}
