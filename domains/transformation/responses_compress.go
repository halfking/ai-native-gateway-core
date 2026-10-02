package transformation

import "encoding/json"

// CompressResponsesInputIfNeeded removes the oldest removable items from an
// OpenAI Responses request when its estimated size exceeds the input budget.
// The input budget is contextWindow-outputReserve. Top-level fields other than
// input are retained, and the latest input item is always retained.
//
// This deliberately handles only the native Responses input string/array
// shape. A string input, an unknown item type, or malformed input is returned
// unchanged (fail-open). function_call and function_call_output items are
// treated as one atomic unit, so trimming cannot orphan a tool result.
func CompressResponsesInputIfNeeded(body []byte, contextWindow, outputReserve int) []byte {
	out, _ := CompressResponsesInputIfNeededWithMeta(body, contextWindow, outputReserve)
	return out
}

// ResponsesInputTrimMeta 是 responses 专属压缩链的 provenance 载荷
// （R34-A1）：此前该链只输出裁剪后的 body，审计无法回答"哪些 input
// item 被删了"。DroppedIndexes 是压缩前 input 数组中被删 item 的原始
// 下标；AlignmentMap（调用侧用 compression.BuildResponsesAlignmentMap
// 基于 before/after 构建）给出完整位置映射，本结构保留精确删除证据，
// 两者互补。
type ResponsesInputTrimMeta struct {
	// Strategy 触发的裁剪策略："responses_input_trim"（proactive
	// candidate-window 路径）或 "responses_input_trim_aggressive"
	//（4xx context-length recovery 路径）。空串=未触发压缩。
	Strategy string `json:"strategy,omitempty"`
	// OriginalItems / KeptItems 压缩前后的 input item 数（数组形态）。
	OriginalItems int `json:"original_items,omitempty"`
	KeptItems     int `json:"kept_items,omitempty"`
	// DroppedIndexes 被删 item 在压缩前 input 数组中的下标（升序）。
	DroppedIndexes []int `json:"dropped_indexes,omitempty"`
	// InputString 形态标记：string input 走 rune 级前缀截断，无 item
	// 粒度，此时 OriginalRunes/KeptRunes 留证。
	InputString  bool `json:"input_string,omitempty"`
	OriginalRunes int `json:"original_runes,omitempty"`
	KeptRunes     int `json:"kept_runes,omitempty"`
}

// CompressResponsesInputIfNeededWithMeta 同 CompressResponsesInputIfNeeded，
// 并返回 provenance；未触发压缩时 meta 为 nil。
func CompressResponsesInputIfNeededWithMeta(body []byte, contextWindow, outputReserve int) ([]byte, *ResponsesInputTrimMeta) {
	return compressResponsesInput(body, contextWindow, outputReserve, defaultTriggerFraction, targetFractionForWindow(contextWindow), strategyResponsesTrim)
}

// CompressResponsesInputAggressively is used after a provider context-length
// rejection. It targets 60% of the usable window and keeps the same structural
// and tool-link safety guarantees as the proactive path.
func CompressResponsesInputAggressively(body []byte, contextWindow, outputReserve int) []byte {
	out, _ := CompressResponsesInputAggressivelyWithMeta(body, contextWindow, outputReserve)
	return out
}

// CompressResponsesInputAggressivelyWithMeta 同
// CompressResponsesInputAggressively，并返回 provenance。
func CompressResponsesInputAggressivelyWithMeta(body []byte, contextWindow, outputReserve int) ([]byte, *ResponsesInputTrimMeta) {
	return compressResponsesInput(body, contextWindow, outputReserve, 0, aggressiveSoftLimitFraction, strategyResponsesTrimAggressive)
}

const (
	strategyResponsesTrim           = "responses_input_trim"
	strategyResponsesTrimAggressive = "responses_input_trim_aggressive"
)

func compressResponsesInput(body []byte, contextWindow, outputReserve int, triggerFraction, targetFraction float64, strategy string) ([]byte, *ResponsesInputTrimMeta) {
	if contextWindow <= 0 {
		return body, nil
	}
	budget := contextWindow - outputReserve
	if budget <= 0 {
		return body, nil
	}

	targetBudget := int(float64(budget) * targetFraction)
	if targetBudget <= 0 {
		return body, nil
	}

	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return body, nil
	}
	rawInput, ok := request["input"]
	if !ok || len(rawInput) == 0 || string(rawInput) == "null" {
		return body, nil
	}
	if triggerFraction > 0 && estimatePromptTokens(body) <= int(float64(budget)*triggerFraction) {
		return body, nil
	}
	var inputString string
	if json.Unmarshal(rawInput, &inputString) == nil {
		return trimResponsesInputString(request, inputString, targetBudget, body, strategy)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawInput, &items); err != nil || len(items) == 0 {
		return body, nil
	}

	units, pairs, ok := responsesCompressionUnits(items)
	if !ok {
		return body, nil
	}

	keep := make([]bool, len(items))
	for i := range keep {
		keep[i] = true
	}
	// The latest item is always retained. If it is one half of a tool pair,
	// the pair is retained as well.
	for _, pair := range pairs {
		if pair[0] == len(items)-1 || pair[1] == len(items)-1 {
			keep[pair[0]] = true
			keep[pair[1]] = true
		}
	}

	for _, unit := range units {
		if estimateResponsesBody(request, items, keep) <= targetBudget {
			break
		}
		removable := true
		for _, index := range unit {
			if index == len(items)-1 || isResponsesProtectedItem(items[index]) || !keep[index] {
				removable = false
				break
			}
		}
		if removable {
			for _, index := range unit {
				keep[index] = false
			}
		}
	}

	if allResponsesItemsKept(keep) {
		return body, nil
	}
	trimmed := make([]json.RawMessage, 0, len(items))
	dropped := make([]int, 0)
	for i, item := range items {
		if keep[i] {
			trimmed = append(trimmed, item)
		} else {
			dropped = append(dropped, i)
		}
	}
	encoded, err := json.Marshal(trimmed)
	if err != nil {
		return body, nil
	}
	request["input"] = encoded
	out, err := json.Marshal(request)
	if err != nil {
		return body, nil
	}
	return out, &ResponsesInputTrimMeta{
		Strategy:       strategy,
		OriginalItems:  len(items),
		KeptItems:      len(trimmed),
		DroppedIndexes: dropped,
	}
}

type responsesCompressionUnit []int

// responsesCompressionUnits validates the supported input item vocabulary and
// returns oldest-first removable units. Pair membership is kept separately so
// a pair containing the latest item can be protected.
func responsesCompressionUnits(items []json.RawMessage) ([]responsesCompressionUnit, [][2]int, bool) {
	calls := make(map[string]int)
	outputs := make(map[string]int)
	for i, raw := range items {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(raw, &item); err != nil || item == nil {
			return nil, nil, false
		}
		var typ, role, callID string
		if rawType := item["type"]; len(rawType) > 0 {
			if err := json.Unmarshal(rawType, &typ); err != nil {
				return nil, nil, false
			}
		}
		if rawRole := item["role"]; len(rawRole) > 0 {
			if err := json.Unmarshal(rawRole, &role); err != nil {
				return nil, nil, false
			}
		}
		if role == "" && (typ == "input_text" || typ == "text" || typ == "message") {
			role = "user"
		}
		switch typ {
		case "function_call":
			if !decodeNonEmptyString(item["call_id"], &callID) {
				_ = decodeNonEmptyString(item["id"], &callID)
			}
			if callID == "" || calls[callID] != 0 {
				return nil, nil, false
			}
			calls[callID] = i + 1
		case "function_call_output":
			if !decodeNonEmptyString(item["call_id"], &callID) || outputs[callID] != 0 || len(item["output"]) == 0 || string(item["output"]) == "null" {
				return nil, nil, false
			}
			outputs[callID] = i + 1
		case "", "message", "input_text", "text":
			if typ == "input_text" || typ == "text" {
				var text string
				if !decodeString(item["text"], &text) {
					return nil, nil, false
				}
			} else if !validResponsesMessageContent(item["content"]) {
				return nil, nil, false
			}
			if typ == "message" && role == "" {
				role = "user"
			}
		case "input_image", "input_audio", "input_file", "computer_screenshot", "computer_call":
			// Multimodal and computer-use items are opaque to the trim policy.
			// They remain atomic input items and may be dropped only as a whole.
			// Do not inspect provider-specific payloads here; preserving unknown
			// nested fields is more important than attempting lossy validation.
		default:
			return nil, nil, false
		}
		if typ == "message" || typ == "" {
			if role != "" && role != "user" && role != "assistant" && role != "system" && role != "developer" {
				return nil, nil, false
			}
		}
	}

	var pairs [][2]int
	for id, callPos := range calls {
		outputPos, ok := outputs[id]
		if !ok || callPos-1 >= outputPos-1 {
			return nil, nil, false
		}
		pairs = append(pairs, [2]int{callPos - 1, outputPos - 1})
	}
	for id := range outputs {
		if _, ok := calls[id]; !ok {
			return nil, nil, false
		}
	}

	pairByIndex := make(map[int][2]int, len(pairs)*2)
	for _, pair := range pairs {
		pairByIndex[pair[0]] = pair
		pairByIndex[pair[1]] = pair
	}
	units := make([]responsesCompressionUnit, 0, len(items))
	for i := range items {
		if pair, ok := pairByIndex[i]; ok {
			if i == pair[0] {
				units = append(units, responsesCompressionUnit{pair[0], pair[1]})
			}
			continue
		}
		units = append(units, responsesCompressionUnit{i})
	}
	return units, pairs, true
}

func decodeString(raw json.RawMessage, dst *string) bool {
	return len(raw) > 0 && json.Unmarshal(raw, dst) == nil
}

func decodeNonEmptyString(raw json.RawMessage, dst *string) bool {
	return decodeString(raw, dst) && *dst != ""
}

func validResponsesMessageContent(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return true
	}
	var blocks []json.RawMessage
	return json.Unmarshal(raw, &blocks) == nil
}

func isResponsesProtectedItem(raw json.RawMessage) bool {
	var item struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return false
	}
	return item.Role == "system" || item.Role == "developer"
}

func estimateResponsesBody(request map[string]json.RawMessage, items []json.RawMessage, keep []bool) int {
	trimmed := make([]json.RawMessage, 0, len(items))
	for i, item := range items {
		if keep[i] {
			trimmed = append(trimmed, item)
		}
	}
	encoded, err := json.Marshal(trimmed)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	copyRequest := make(map[string]json.RawMessage, len(request))
	for key, value := range request {
		copyRequest[key] = value
	}
	copyRequest["input"] = encoded
	body, err := json.Marshal(copyRequest)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return estimatePromptTokens(body)
}

func allResponsesItemsKept(keep []bool) bool {
	for _, value := range keep {
		if !value {
			return false
		}
	}
	return true
}

// RewriteResponsesModel changes only the top-level model field. It returns the
// original bytes when decoding fails or the requested model is already present.
func RewriteResponsesModel(bodyBytes []byte, model string) []byte {
	if len(bodyBytes) == 0 || model == "" {
		return bodyBytes
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		return bodyBytes
	}
	var current string
	if raw, ok := envelope["model"]; ok {
		_ = json.Unmarshal(raw, &current)
	}
	if current == model {
		return bodyBytes
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return bodyBytes
	}
	envelope["model"] = encoded
	out, err := json.Marshal(envelope)
	if err != nil {
		return bodyBytes
	}
	return out
}

// CompressResponsesIfNeeded applies the standard provider-aware threshold to a
// native Responses request while preserving input-item and tool-pair integrity.
func CompressResponsesIfNeeded(body []byte, contextWindow int) []byte {
	return CompressResponsesInputIfNeeded(body, contextWindow, 0)
}

// CompressResponsesAggressively is the context-overflow recovery variant for
// native Responses requests. It uses the same safe atomic trimming policy.
func CompressResponsesAggressively(body []byte, contextWindow int) []byte {
	return CompressResponsesInputAggressively(body, contextWindow, 0)
}

func trimResponsesInputString(request map[string]json.RawMessage, input string, targetTokens int, original []byte, strategy string) ([]byte, *ResponsesInputTrimMeta) {
	originalRunes := len([]rune(input))
	runes := []rune(input)
	if len(runes) <= 1 {
		return original, nil
	}
	for len(runes) > 1 {
		encoded, err := json.Marshal(string(runes))
		if err != nil {
			return original, nil
		}
		candidate := make(map[string]json.RawMessage, len(request))
		for key, value := range request {
			candidate[key] = value
		}
		candidate["input"] = encoded
		out, err := json.Marshal(candidate)
		if err != nil {
			return original, nil
		}
		if estimatePromptTokens(out) <= targetTokens {
			return out, &ResponsesInputTrimMeta{
				Strategy:      strategy,
				InputString:   true,
				OriginalRunes: originalRunes,
				KeptRunes:     len(runes),
			}
		}
		runes = runes[len(runes)/2:]
	}
	return original, nil
}
