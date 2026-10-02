package outputcompliance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// visibleTextField points only at client-visible text. IDs, usage, media data,
// URLs and protocol metadata are deliberately outside this list.
type visibleTextField struct {
	parent     map[string]any
	key        string
	lane       string
	immutable  bool   // A signed thinking block cannot be rewritten in place.
	label      string // Credential key context for structured tool input leaves.
	array      []any
	index      int
	checkValue any
	checkOnly  bool
}

func (f visibleTextField) text() (string, bool) {
	if f.checkOnly {
		raw, err := json.Marshal(f.checkValue)
		return string(raw), err == nil
	}
	if f.array != nil {
		text, ok := f.array[f.index].(string)
		return text, ok
	}
	text, ok := f.parent[f.key].(string)
	return text, ok
}
func (f visibleTextField) set(text string) {
	if f.array != nil {
		f.array[f.index] = text
	} else {
		f.parent[f.key] = text
	}
}

// transformVisibleJSON checks each text lane independently. Checking the whole
// wire JSON can both miss escaped text and mistake metadata for model output.
// Unknown JSON fields survive a rewrite, including vendor extensions.
func transformVisibleJSON(body []byte, stream bool, blockToolRewrite bool, transform func(string, string) (string, int, bool, error)) ([]byte, int, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, 0, false, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values in response")
		}
		return nil, 0, false, err
	}
	if root == nil {
		return nil, 0, false, errors.New("response is not a JSON object")
	}

	fields := collectVisibleText(root, stream)
	modified := false
	issues := 0
	for _, field := range fields {
		if field.checkOnly && !blockToolRewrite {
			continue
		}
		original, ok := field.text()
		if !ok || original == "" {
			continue
		}
		replacement, count, blocked, err := transform(field.label, original)
		if err != nil || blocked {
			return nil, issues + count, blocked, err
		}
		issues += count
		if replacement != original {
			if field.immutable || (blockToolRewrite && (isToolArgumentLane(field.lane) || field.label != "")) {
				return nil, issues, true, nil
			}
			if field.label == "__tool_json" && json.Valid([]byte(original)) && !json.Valid([]byte(replacement)) {
				return nil, issues, true, nil
			}
			field.set(replacement)
			modified = true
		}
	}
	if !modified {
		return body, issues, false, nil
	}
	out, err := json.Marshal(root)
	return out, issues, false, err
}

func collectVisibleText(root map[string]any, stream bool) []visibleTextField {
	fields := make([]visibleTextField, 0, 4)
	add := func(parent map[string]any, key, lane string, immutable bool) {
		if _, ok := parent[key].(string); ok {
			label := ""
			if isToolArgumentLane(lane) {
				label = "__tool_text"
				if isJSONToolArgumentLane(lane) {
					label = "__tool_json"
				}
			}
			fields = append(fields, visibleTextField{parent: parent, key: key, lane: lane, label: label, immutable: immutable})
		}
	}
	addToolInput := func(parent map[string]any, key, lane string) {
		add(parent, key, lane, false)
		if value, ok := parent[key]; ok && value != nil {
			if _, isText := value.(string); !isText {
				fields = append(fields, visibleTextField{lane: lane, label: "__tool_json", checkValue: value, checkOnly: true})
				collectToolInputStrings(value, lane, &fields)
			}
		}
	}
	var addTextParts func(any, string, bool)
	addTextParts = func(raw any, lane string, thinkingImmutable bool) {
		parts, ok := raw.([]any)
		if !ok {
			return
		}
		for index, part := range parts {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := p["type"].(string)
			partLane := fmt.Sprintf("%s.part.%d.%s", lane, index, typ)
			switch typ {
			case "text", "output_text", "refusal", "input_text", "summary_text", "reasoning_text":
				add(p, "text", partLane+".text", false)
				add(p, "refusal", partLane+".refusal", false)
			case "output_audio":
				add(p, "transcript", partLane+".transcript", false)
			case "thinking":
				_, signed := p["signature"].(string)
				add(p, "thinking", partLane+".thinking", thinkingImmutable || signed)
			case "tool_use":
				addToolInput(p, "input", partLane+".input")
			}
		}
	}
	if choices, ok := root["choices"].([]any); ok {
		for position, raw := range choices {
			choice, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			choiceLane := fmt.Sprintf("chat.choice.%s", streamLaneComponent(choice, "index", position))
			add(choice, "text", choiceLane+".text", false)
			for _, key := range []string{"message", "delta"} {
				msg, ok := choice[key].(map[string]any)
				if !ok {
					continue
				}
				msgLane := choiceLane + "." + key
				add(msg, "content", msgLane+".content", false)
				add(msg, "refusal", msgLane+".refusal", false)
				add(msg, "reasoning_content", msgLane+".reasoning_content", false)
				if audio, ok := msg["audio"].(map[string]any); ok {
					add(audio, "transcript", msgLane+".audio.transcript", false)
				}
				addTextParts(msg["content"], msgLane+".content", false)
				if calls, ok := msg["tool_calls"].([]any); ok {
					for callPosition, rawCall := range calls {
						if call, ok := rawCall.(map[string]any); ok {
							if function, ok := call["function"].(map[string]any); ok {
								callLane := fmt.Sprintf("%s.tool.%s.arguments", msgLane, streamLaneComponent(call, "index", callPosition))
								add(function, "arguments", callLane, false)
							}
						}
					}
				}
				if function, ok := msg["function_call"].(map[string]any); ok {
					add(function, "arguments", msgLane+".function_call.arguments", false)
				}
			}
		}
	}
	if _, ok := root["completion"].(string); ok {
		add(root, "completion", "completion", false)
	}
	// Provider error envelopes are visible output too and can echo credentials.
	for _, key := range []string{"message", "detail"} {
		add(root, key, "error."+key, false)
	}
	if providerError, ok := root["error"].(map[string]any); ok {
		for _, key := range []string{"message", "detail"} {
			add(providerError, key, "error."+key, false)
		}
	}
	// Some Responses providers emit a convenience output_text in addition to
	// output[]. The restore hook also visits it, so compliance must inspect it.
	if _, ok := root["output_text"].(string); ok {
		add(root, "output_text", "responses.output_text", false)
	}
	if typ, _ := root["type"].(string); typ == "message" {
		addTextParts(root["content"], "messages", true)
		add(root, "content", "messages.content", false)
		add(root, "refusal", "messages.refusal", false)
	}
	addOutputItem := func(item map[string]any, position int) {
		typ, _ := item["type"].(string)
		itemLane := fmt.Sprintf("responses.output.%s", streamLaneComponent(item, "id", position))
		switch typ {
		case "message":
			addTextParts(item["content"], itemLane, false)
			add(item, "refusal", itemLane+".refusal", false)
		case "function_call", "custom_tool_call":
			add(item, "arguments", itemLane+".arguments", false)
			addToolInput(item, "input", itemLane+".input")
		case "reasoning":
			addTextParts(item["summary"], itemLane+".summary", false)
			addTextParts(item["content"], itemLane+".content", false)
		}
	}
	if output, ok := root["output"].([]any); ok {
		for position, raw := range output {
			if item, ok := raw.(map[string]any); ok {
				addOutputItem(item, position)
			}
		}
	}
	if !stream {
		return fields
	}
	// Native streaming events carry the visible text in delta fields. The
	// terminal response object is also inspected when present.
	if delta, ok := root["delta"].(map[string]any); ok {
		index := streamLaneComponent(root, "index", 0)
		for _, key := range []string{"text", "thinking", "partial_json", "input", "refusal", "content"} {
			add(delta, key, "anthropic.block."+index+"."+key, key == "thinking")
		}
	}
	typ, _ := root["type"].(string)
	itemID := streamLaneComponent(root, "item_id", 0)
	outputIndex := streamLaneComponent(root, "output_index", 0)
	contentIndex := streamLaneComponent(root, "content_index", 0)
	summaryIndex := streamLaneComponent(root, "summary_index", 0)
	responseItemLane := fmt.Sprintf("responses.%s.%s", itemID, outputIndex)
	responseContentLane := fmt.Sprintf("%s.content.%s", responseItemLane, contentIndex)
	responseSummaryLane := fmt.Sprintf("%s.summary.%s", responseItemLane, summaryIndex)
	addResponsePart := func(part map[string]any, lane string) {
		partType, _ := part["type"].(string)
		switch partType {
		case "output_text", "input_text", "text":
			add(part, "text", lane+".output_text", false)
		case "refusal":
			add(part, "refusal", lane+".refusal", false)
			add(part, "text", lane+".refusal", false)
		case "output_audio":
			add(part, "transcript", lane+".audio_transcript", false)
		case "summary_text":
			add(part, "text", lane+".reasoning_summary_text", false)
		default:
			// Provider extensions sometimes omit part.type while retaining
			// standard visible fields. Keep them in the matching delta lane.
			add(part, "text", lane+".output_text", false)
			add(part, "refusal", lane+".refusal", false)
			add(part, "transcript", lane+".audio_transcript", false)
		}
	}
	switch typ {
	case "response.output_text.delta":
		add(root, "delta", responseContentLane+".output_text", false)
	case "response.refusal.delta":
		add(root, "delta", responseContentLane+".refusal", false)
	case "response.audio_transcript.delta":
		add(root, "delta", responseContentLane+".audio_transcript", false)
	case "response.reasoning_text.delta":
		add(root, "delta", responseContentLane+".reasoning_text", false)
	case "response.reasoning_summary_text.delta":
		add(root, "delta", responseSummaryLane+".reasoning_summary_text", false)
	case "response.function_call_arguments.delta":
		add(root, "delta", responseItemLane+".arguments", false)
	case "response.custom_tool_call_input.delta":
		add(root, "delta", responseItemLane+".custom_input", false)
	case "response.output_text.done":
		add(root, "text", responseContentLane+".output_text.snapshot", false)
	case "response.refusal.done":
		add(root, "refusal", responseContentLane+".refusal.snapshot", false)
		add(root, "text", responseContentLane+".refusal.snapshot", false)
	case "response.reasoning_text.done":
		add(root, "text", responseContentLane+".reasoning_text.snapshot", false)
	case "response.reasoning_summary_text.done":
		add(root, "text", responseSummaryLane+".reasoning_summary_text.snapshot", false)
	case "response.function_call_arguments.done":
		add(root, "arguments", responseItemLane+".arguments.snapshot", false)
	case "response.custom_tool_call_input.done":
		add(root, "input", responseItemLane+".custom_input.snapshot", false)
	case "response.audio_transcript.done":
		add(root, "transcript", responseContentLane+".audio_transcript.snapshot", false)
		add(root, "text", responseContentLane+".audio_transcript.snapshot", false)
	case "content_block_start":
		if block, ok := root["content_block"].(map[string]any); ok {
			blockLane := "anthropic.block." + streamLaneComponent(root, "index", 0)
			add(block, "text", blockLane+".text", false)
			add(block, "thinking", blockLane+".thinking", true)
			if typ, _ := block["type"].(string); typ == "tool_use" {
				addToolInput(block, "input", blockLane+".input")
			}
		}
	case "response.content_part.added", "response.content_part.done":
		if part, ok := root["part"].(map[string]any); ok {
			lane := responseContentLane
			if typ == "response.content_part.done" {
				lane += ".snapshot"
			}
			addResponsePart(part, lane)
		}
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		if part, ok := root["part"].(map[string]any); ok {
			lane := responseSummaryLane
			if typ == "response.reasoning_summary_part.done" {
				lane += ".snapshot"
			}
			addResponsePart(part, lane)
		}
	case "response.output_item.added", "response.output_item.done":
		if item, ok := root["item"].(map[string]any); ok {
			if typ == "response.output_item.done" {
				// The done item is a cumulative snapshot, not another delta.
				addOutputItem(item, 0)
				break
			}
			initialItemID := streamLaneComponent(item, "id", 0)
			if initialItemID == "0" {
				initialItemID = itemID
			}
			initialLane := fmt.Sprintf("responses.%s.%s", initialItemID, outputIndex)
			switch itemType, _ := item["type"].(string); itemType {
			case "message":
				add(item, "refusal", initialLane+".content.0.refusal", false)
				if content, ok := item["content"].([]any); ok {
					for index, rawPart := range content {
						if part, ok := rawPart.(map[string]any); ok {
							addResponsePart(part, fmt.Sprintf("%s.content.%d", initialLane, index))
						}
					}
				}
			case "function_call":
				add(item, "arguments", initialLane+".arguments", false)
			case "custom_tool_call":
				add(item, "input", initialLane+".custom_input", false)
			case "reasoning":
				if summary, ok := item["summary"].([]any); ok {
					for index, rawPart := range summary {
						if part, ok := rawPart.(map[string]any); ok {
							addResponsePart(part, fmt.Sprintf("%s.summary.%d", initialLane, index))
						}
					}
				}
			}
		}
	}
	if resp, ok := root["response"].(map[string]any); ok {
		fields = append(fields, collectVisibleText(resp, false)...)
	}
	return fields
}

func collectToolInputStrings(raw any, lane string, fields *[]visibleTextField) {
	collectToolInputStringsWithLabel(raw, lane, "", fields)
}

func collectToolInputStringsWithLabel(raw any, lane, label string, fields *[]visibleTextField) {
	switch value := raw.(type) {
	case map[string]any:
		for key, child := range value {
			if IsOpaqueToolValue(key, child) {
				continue
			}
			if typ, ok := child.(string); key == "type" && ok && isMediaContentType(typ) {
				continue
			}
			switch child.(type) {
			case string:
				*fields = append(*fields, visibleTextField{parent: value, key: key, label: key, lane: lane + "." + key})
			default:
				collectToolInputStringsWithLabel(child, lane+"."+key, key, fields)
			}
		}
	case []any:
		for index, child := range value {
			if _, ok := child.(string); ok {
				*fields = append(*fields, visibleTextField{array: value, index: index, label: label, lane: fmt.Sprintf("%s.%d", lane, index)})
			} else {
				collectToolInputStringsWithLabel(child, fmt.Sprintf("%s.%d", lane, index), label, fields)
			}
		}
	}
}

func streamLaneComponent(object map[string]any, key string, fallback int) string {
	value, ok := object[key]
	if !ok {
		return fmt.Sprint(fallback)
	}
	switch typed := value.(type) {
	case string:
		if typed != "" {
			return typed
		}
	case json.Number:
		return typed.String()
	case float64:
		return fmt.Sprint(typed)
	}
	return fmt.Sprint(fallback)
}

func isMediaContentType(typ string) bool {
	switch typ {
	case "image", "image_url", "input_image", "output_image", "audio", "input_audio", "output_audio", "video", "file", "input_file", "document":
		return true
	}
	return false
}

// IsOpaqueToolField identifies binary tool payloads; a media discriminator
// never exempts sibling textual credentials from inspection.
func IsOpaqueToolField(key string) bool {
	switch key {
	case "data", "base64", "file_data", "audio", "image":
		return true
	}
	return false
}

// Only scalar binary payloads are opaque. A structured value can contain
// ordinary tool arguments and must still be traversed.
func IsOpaqueToolValue(key string, value any) bool {
	_, scalar := value.(string)
	return scalar && IsOpaqueToolField(key)
}
