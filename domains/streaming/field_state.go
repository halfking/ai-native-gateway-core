package streaming

import (
	"encoding/json"
)

// FieldState represents the state of a JSON field in a request.
// It distinguishes between three fundamentally different cases:
//   - FieldNotProvided: client didn't send the field at all
//   - FieldNull: client explicitly sent JSON null
//   - FieldProvided: client sent an actual value (could be empty array/object)
//
// This distinction is crucial for API semantics:
//   - Not provided → use default or inherit from context
//   - Explicit null → clear/reset the value
//   - Provided value → use the new value
type FieldState int

const (
	// FieldNotProvided means the client didn't include this field in the request.
	// For optional fields, this typically means "use default" or "inherit from context".
	// For required fields, this should be rejected with a validation error.
	FieldNotProvided FieldState = iota

	// FieldNull means the client explicitly sent JSON null for this field.
	// For optional fields, this typically means "clear/reset this value".
	// For required fields, this should be rejected with a validation error.
	FieldNull

	// FieldProvided means the client sent an actual value (even if empty like [] or {}).
	// This is the normal case for both required and optional fields.
	FieldProvided
)

// GetFieldState determines the state of a json.RawMessage field.
//
// Examples:
//   - Request: {"model":"gpt-4"}              → GetFieldState(messages) = FieldNotProvided
//   - Request: {"model":"gpt-4","messages":null}  → GetFieldState(messages) = FieldNull
//   - Request: {"model":"gpt-4","messages":[]}    → GetFieldState(messages) = FieldProvided
//   - Request: {"model":"gpt-4","messages":{}}    → GetFieldState(messages) = FieldProvided
//
// Implementation notes:
//   - json.RawMessage with omitempty: nil when field not sent, "null" bytes when null sent
//   - len(raw) == 0: field was not provided (omitempty suppressed it)
//   - len(raw) == 4 && string(raw) == "null": field was explicitly set to null
//   - len(raw) > 0 && string(raw) != "null": field has a value (even if empty array/object)
func GetFieldState(raw json.RawMessage) FieldState {
	if len(raw) == 0 {
		return FieldNotProvided
	}
	if string(raw) == "null" {
		return FieldNull
	}
	return FieldProvided
}

// String returns a human-readable representation of the field state.
func (fs FieldState) String() string {
	switch fs {
	case FieldNotProvided:
		return "not_provided"
	case FieldNull:
		return "null"
	case FieldProvided:
		return "provided"
	default:
		return "unknown"
	}
}

// ToStringPtr converts a json.RawMessage to *string based on its state.
// This is useful when storing fields in the database:
//   - FieldNotProvided → nil (database NULL, meaning "not provided")
//   - FieldNull → "null" (JSON null stored as string)
//   - FieldProvided → actual content
//
// Example:
//
//	var messagesPtr *string = ToStringPtr(reqBody.Messages)
//	// Store messagesPtr in database, preserving the semantic difference
func ToStringPtr(raw json.RawMessage) *string {
	state := GetFieldState(raw)
	switch state {
	case FieldNotProvided:
		return nil
	case FieldNull:
		v := "null"
		return &v
	case FieldProvided:
		v := string(raw)
		return &v
	default:
		return nil
	}
}

// ValidateRequiredField validates that a required field is provided and not null.
// Returns an error message if validation fails, or empty string if valid.
//
// Example:
//
//	if errMsg := ValidateRequiredField(reqBody.Messages, "messages"); errMsg != "" {
//	    return errorResponse("invalid_request", errMsg)
//	}
func ValidateRequiredField(raw json.RawMessage, fieldName string) string {
	state := GetFieldState(raw)
	switch state {
	case FieldNotProvided:
		return fieldName + " field is required"
	case FieldNull:
		return fieldName + " cannot be null"
	case FieldProvided:
		return "" // Valid
	default:
		return fieldName + " has unknown state"
	}
}

// ValidateNonEmptyArray validates that a field is a non-empty JSON array.
// Returns an error message if validation fails, or empty string if valid.
//
// Example:
//
//	if errMsg := ValidateNonEmptyArray(reqBody.Messages, "messages"); errMsg != "" {
//	    return errorResponse("invalid_request", errMsg)
//	}
func ValidateNonEmptyArray(raw json.RawMessage, fieldName string) string {
	state := GetFieldState(raw)

	// First check field state
	if state == FieldNotProvided {
		return fieldName + " field is required"
	}
	if state == FieldNull {
		return fieldName + " cannot be null"
	}

	// Then validate it's a non-empty array
	var arr []interface{}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return fieldName + " must be a valid JSON array, not an object or other type"
	}

	if len(arr) == 0 {
		return fieldName + " array cannot be empty"
	}

	return "" // Valid
}

// HasUserMessage checks if a messages array contains at least one message with role="user".
// Returns true if at least one user message is found.
//
// Example:
//
//	if !HasUserMessage(reqBody.Messages) {
//	    return errorResponse("no_user_message", "messages must contain at least one user message")
//	}
func HasUserMessage(raw json.RawMessage) bool {
	var messages []map[string]interface{}
	if err := json.Unmarshal(raw, &messages); err != nil {
		return false
	}

	for _, msg := range messages {
		if role, ok := msg["role"].(string); ok && role == "user" {
			return true
		}
	}

	return false
}
