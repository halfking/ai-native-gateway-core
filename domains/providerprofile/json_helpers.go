package providerprofile

import "encoding/json"

// marshalJSON 将对象序列化为JSON字节
func marshalJSON(v interface{}) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
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
