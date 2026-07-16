// request_validator.go — 请求格式校验器实现
package executors

import (
	"context"
	"encoding/json"
	"fmt"
)

// RequestValidatorImpl 请求格式校验器实现
type RequestValidatorImpl struct {
	StrictValidation bool
}

// NewRequestValidator 创建新的校验器
func NewRequestValidator(strict bool) *RequestValidatorImpl {
	return &RequestValidatorImpl{
		StrictValidation: strict,
	}
}

// Validate 校验请求体
func (v *RequestValidatorImpl) Validate(ctx context.Context, requestBody []byte) (*ValidationResult, error) {
	result := &ValidationResult{Valid: true}

	var reqBody map[string]interface{}
	if err := json.Unmarshal(requestBody, &reqBody); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, ValidationError{
			Field:   "body",
			Code:    "invalid_json",
			Message: fmt.Sprintf("invalid JSON: %v", err),
		})
		return result, fmt.Errorf("invalid JSON: %w", err)
	}

	errors := v.validateRequiredFields(reqBody)
	result.Errors = append(result.Errors, errors...)

	if len(errors) > 0 {
		result.Valid = false
		if v.StrictValidation {
			return result, fmt.Errorf("missing required fields: %d errors", len(errors))
		}
	}

	if len(requestBody) > 500*1024 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("large request body: %d bytes (>500KB)", len(requestBody)))
	}

	return result, nil
}

func (v *RequestValidatorImpl) validateRequiredFields(body map[string]interface{}) []ValidationError {
	var errors []ValidationError

	if model, ok := body["model"]; !ok || model == "" {
		errors = append(errors, ValidationError{
			Field:   "model",
			Code:    "missing_required_field",
			Message: "model field is required",
		})
	}

	messages, ok := body["messages"]
	if !ok {
		errors = append(errors, ValidationError{
			Field:   "messages",
			Code:    "missing_required_field",
			Message: "messages field is required",
		})
		return errors
	}

	messagesArray, ok := messages.([]interface{})
	if !ok {
		errors = append(errors, ValidationError{
			Field:   "messages",
			Code:    "invalid_type",
			Message: "messages must be an array",
		})
		return errors
	}

	if len(messagesArray) == 0 {
		errors = append(errors, ValidationError{
			Field:   "messages",
			Code:    "empty_array",
			Message: "messages array cannot be empty",
		})
		return errors
	}

	for i, msg := range messagesArray {
		msgMap, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}

		if _, ok := msgMap["role"]; !ok {
			errors = append(errors, ValidationError{
				Field:   fmt.Sprintf("messages[%d].role", i),
				Code:    "missing_required_field",
				Message: "message role is required",
			})
		}

		if _, ok := msgMap["content"]; !ok {
			errors = append(errors, ValidationError{
				Field:   fmt.Sprintf("messages[%d].content", i),
				Code:    "missing_required_field",
				Message: "message content is required",
			})
		}
	}

	return errors
}
