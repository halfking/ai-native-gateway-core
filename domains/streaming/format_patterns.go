package streaming

// Predefined format patterns for common clients.

// createOpenAIPattern creates the standard OpenAI API format pattern.
func createOpenAIPattern() *FormatPattern {
	return &FormatPattern{
		ID:         "openai-standard",
		Name:       "OpenAI Standard API",
		ClientHint: []string{"openai-python", "openai-node", "openai/"},
		Version:    "1.0.0",
		Features: FormatFeatures{
			RequiredFields: []string{"model", "messages"},
			OptionalFields: []string{"temperature", "top_p", "n", "stream", "tools", "max_tokens"},
			FieldTypes: map[string]string{
				"model":       "string",
				"messages":    "array",
				"temperature": "number",
				"stream":      "boolean",
				"tools":       "array",
			},
		},
		KnownIssues: []FormatIssue{},
		Fixes:       []FormatFix{},
	}
}

// createOpenCodePattern creates the OpenCode CLI format pattern.
func createOpenCodePattern() *FormatPattern {
	return &FormatPattern{
		ID:         "opencode-v1",
		Name:       "OpenCode CLI",
		ClientHint: []string{"opencode"},
		Version:    "1.0.0",
		Features: FormatFeatures{
			RequiredFields: []string{"model"},
			OptionalFields: []string{"messages", "temperature", "stream", "max_tokens"},
			FieldTypes: map[string]string{
				"model":    "string",
				"messages": "array",
				"stream":   "boolean",
			},
		},
		KnownIssues: []FormatIssue{
			{
				Type:        "empty_object_messages",
				Field:       "messages",
				Description: "Sends messages: {} in continuation scenarios",
				Frequency:   50,
			},
			{
				Type:        "missing_messages",
				Field:       "messages",
				Description: "Missing messages field in some requests",
				Frequency:   20,
			},
			{
				Type:        "string_messages",
				Field:       "messages",
				Description: "Sends messages as string instead of array",
				Frequency:   30, // Increased frequency
			},
		},
		Fixes: []FormatFix{
			{
				IssueType:   "empty_object_messages",
				FixFunc:     fixEmptyObjectMessages,
				Description: "Convert empty object {} to empty array []",
			},
			{
				IssueType:   "string_messages",
				FixFunc:     fixStringMessages,
				Description: "Convert string message to proper message array",
			},
		},
	}
}

// createAnthropicPattern creates the Anthropic Claude API format pattern.
func createAnthropicPattern() *FormatPattern {
	return &FormatPattern{
		ID:         "anthropic-claude",
		Name:       "Anthropic Claude API",
		ClientHint: []string{"anthropic-sdk", "claude"},
		Version:    "1.0.0",
		Features: FormatFeatures{
			RequiredFields: []string{"model", "messages"},
			OptionalFields: []string{"max_tokens", "temperature", "system", "stream"},
			FieldTypes: map[string]string{
				"model":      "string",
				"messages":   "array",
				"max_tokens": "number",
				"system":     "string",
			},
			Markers: []string{"anthropic_version"},
		},
		KnownIssues: []FormatIssue{},
		Fixes:       []FormatFix{},
	}
}

// Predefined fix functions

// fixEmptyObjectMessages converts empty object {} to empty array [].
func fixEmptyObjectMessages(body map[string]any) (map[string]any, bool, error) {
	if msg, ok := body["messages"]; ok {
		// Check if it's an empty object
		if msgMap, isMap := msg.(map[string]any); isMap && len(msgMap) == 0 {
			body["messages"] = []any{}
			return body, true, nil
		}
	}
	return body, false, nil
}

// fixStringMessages converts string message to proper message array.
func fixStringMessages(body map[string]any) (map[string]any, bool, error) {
	if msg, ok := body["messages"]; ok {
		// Check if it's a string
		if msgStr, isStr := msg.(string); isStr && msgStr != "" {
			body["messages"] = []any{
				map[string]any{
					"role":    "user",
					"content": msgStr,
				},
			}
			return body, true, nil
		}
	}
	return body, false, nil
}

// fixMissingModel adds a default model if missing.
func fixMissingModel(body map[string]any) (map[string]any, bool, error) {
	if _, ok := body["model"]; !ok {
		body["model"] = "gpt-3.5-turbo"
		return body, true, nil
	}
	return body, false, nil
}

// fixMissingMessages adds an empty messages array if missing.
func fixMissingMessages(body map[string]any) (map[string]any, bool, error) {
	if _, ok := body["messages"]; !ok {
		body["messages"] = []any{}
		return body, true, nil
	}
	return body, false, nil
}

// normalizeStreamField standardizes stream field to boolean.
func normalizeStreamField(body map[string]any) (map[string]any, bool, error) {
	if stream, ok := body["stream"]; ok {
		changed := false

		// Convert string "true"/"false" to boolean
		if streamStr, isStr := stream.(string); isStr {
			body["stream"] = (streamStr == "true" || streamStr == "1" || streamStr == "yes")
			changed = true
		}

		// Convert number 1/0 to boolean
		if streamNum, isNum := stream.(float64); isNum {
			body["stream"] = (streamNum != 0)
			changed = true
		}

		return body, changed, nil
	}
	return body, false, nil
}

// fixNullMessages converts null messages to empty array.
func fixNullMessages(body map[string]any) (map[string]any, bool, error) {
	if msg, ok := body["messages"]; ok {
		if msg == nil {
			body["messages"] = []any{}
			return body, true, nil
		}
	}
	return body, false, nil
}

// extractContentFromSingleMessage extracts content if messages is a single object instead of array.
func extractContentFromSingleMessage(body map[string]any) (map[string]any, bool, error) {
	if msg, ok := body["messages"]; ok {
		// Check if it's a single message object (not array)
		if msgMap, isMap := msg.(map[string]any); isMap {
			// If it has "role" and "content", it's likely a single message
			if role, hasRole := msgMap["role"].(string); hasRole {
				if content, hasContent := msgMap["content"]; hasContent {
					// Wrap it in an array
					body["messages"] = []any{
						map[string]any{
							"role":    role,
							"content": content,
						},
					}
					return body, true, nil
				}
			}
		}
	}
	return body, false, nil
}

// mergeToolsIntoMessages merges separate "tools" field into messages if needed.
func mergeToolsIntoMessages(body map[string]any) (map[string]any, bool, error) {
	// This is a more complex fix that might be needed for certain clients
	// that send tools separately but expect them in messages
	// Implementation depends on specific client behavior
	return body, false, nil
}

// removeUnknownFields removes fields that are not part of the standard API.
func removeUnknownFields(body map[string]any) (map[string]any, bool, error) {
	knownFields := map[string]bool{
		"model":                 true,
		"messages":              true,
		"temperature":           true,
		"top_p":                 true,
		"n":                     true,
		"stream":                true,
		"stop":                  true,
		"max_tokens":            true,
		"max_completion_tokens": true,
		"presence_penalty":      true,
		"frequency_penalty":     true,
		"logit_bias":            true,
		"user":                  true,
		"tools":                 true,
		"tool_choice":           true,
		"response_format":       true,
		"seed":                  true,
		"logprobs":              true,
		"top_logprobs":          true,
		"parallel_tool_calls":   true,
		"service_tier":          true,
		"store":                 true,
		"metadata":              true,
		"reasoning_effort":      true,
		// Gateway-specific fields
		"gw_session_id": true,
		"gw_task_id":    true,
	}

	changed := false
	for field := range body {
		if !knownFields[field] {
			delete(body, field)
			changed = true
		}
	}

	return body, changed, nil
}

// Helper function to create a comprehensive fix pattern
func createComprehensiveFixPattern() *FormatPattern {
	return &FormatPattern{
		ID:         "auto-fix-v1",
		Name:       "Comprehensive Auto-Fix",
		ClientHint: []string{},
		Version:    "1.0.0",
		Features: FormatFeatures{
			RequiredFields: []string{"model"},
			OptionalFields: []string{"messages"},
			FieldTypes:     map[string]string{},
		},
		KnownIssues: []FormatIssue{
			{Type: "empty_object_messages", Field: "messages"},
			{Type: "string_messages", Field: "messages"},
			{Type: "missing_messages", Field: "messages"},
			{Type: "null_messages", Field: "messages"},
			{Type: "single_message_object", Field: "messages"},
		},
		Fixes: []FormatFix{
			{IssueType: "empty_object_messages", FixFunc: fixEmptyObjectMessages, Description: "Convert {} to []"},
			{IssueType: "string_messages", FixFunc: fixStringMessages, Description: "Convert string to message array"},
			{IssueType: "missing_messages", FixFunc: fixMissingMessages, Description: "Add empty messages array"},
			{IssueType: "null_messages", FixFunc: fixNullMessages, Description: "Convert null to []"},
			{IssueType: "single_message_object", FixFunc: extractContentFromSingleMessage, Description: "Wrap single message in array"},
			{IssueType: "normalize_stream", FixFunc: normalizeStreamField, Description: "Normalize stream field"},
		},
	}
}
