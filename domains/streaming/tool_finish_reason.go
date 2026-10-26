package streaming

import (
	"encoding/json"
	"sort"
)

type toolFinishStreamState struct {
	named, ended map[int]bool
	header       map[string]json.RawMessage
}

func (n *Normalizer) forStream() *Normalizer {
	if n == nil {
		return nil
	}
	copy := *n
	copy.toolFinish = &toolFinishStreamState{named: map[int]bool{}, ended: map[int]bool{}, header: map[string]json.RawMessage{}}
	return &copy
}

func hasNamedChatTool(message json.RawMessage) bool {
	var msg struct {
		Calls []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal(message, &msg) != nil {
		return false
	}
	for _, call := range msg.Calls {
		if call.Function.Name != "" {
			return true
		}
	}
	return false
}

func (s *toolFinishStreamState) finish(done []byte) []byte {
	var indexes []int
	for index := range s.named {
		if !s.ended[index] {
			indexes = append(indexes, index)
		}
	}
	if len(indexes) == 0 {
		return done
	}
	sort.Ints(indexes)
	choices := make([]map[string]any, 0, len(indexes))
	for _, index := range indexes {
		choices = append(choices, map[string]any{"index": index, "delta": map[string]any{}, "finish_reason": "tool_calls"})
		s.ended[index] = true
	}
	frame := map[string]any{"object": "chat.completion.chunk", "choices": choices}
	for _, key := range []string{"id", "model", "created"} {
		if raw, ok := s.header[key]; ok {
			frame[key] = raw
		}
	}
	body, err := json.Marshal(frame)
	if err != nil {
		return done
	}
	return append(append([]byte("data: "), append(body, '\n', '\n')...), done...)
}
