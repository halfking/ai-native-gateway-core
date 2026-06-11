package relay

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/routing"
)

// TestExecParamsExposesToolsRequestedField ensures the executor's public
// ExecParams struct carries the ToolsRequested flag that the chat
// completions handler fills via requestHasTools(bodyBytes).  If a future
// refactor removes or renames the field, this test fails to compile
// before the wire-level regression even ships.
func TestExecParamsExposesToolsRequestedField(t *testing.T) {
	var p routing.ExecParams
	p.ToolsRequested = true
	if !p.ToolsRequested {
		t.Fatalf("ExecParams.ToolsRequested field is not settable to true")
	}
}

// TestExecutorExposesXMLCoerceNonStreamField ensures the executor struct
// has a settable XMLCoerceNonStream callback slot.  Without it, the
// non-stream chat response path cannot run the XML→tool_calls coercion
// that the chat completions handler relies on.
func TestExecutorExposesXMLCoerceNonStreamField(t *testing.T) {
	ex := routing.NewExecutor(nil, nil, nil, nil, nil, nil, nil, nil)
	if ex.XMLCoerceNonStream != nil {
		t.Fatalf("default XMLCoerceNonStream should be nil")
	}
	ex.XMLCoerceNonStream = func(body []byte, _ bool) []byte { return body }
	if ex.XMLCoerceNonStream == nil {
		t.Fatalf("XMLCoerceNonStream field is not settable")
	}
}
