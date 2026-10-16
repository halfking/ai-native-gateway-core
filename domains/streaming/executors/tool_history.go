package executors

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/toolhistory"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

// repairOutboundToolHistory completes the actual outbound dialect and classifies
// ambiguity as a local request defect, so routing cannot penalize a provider.
func repairOutboundToolHistory(params *ExecParams, cand provider.Candidate, body []byte, protocol string) ([]byte, error) {
	model := cand.RawModel
	if params != nil {
		model = resolveOutboundModel(params, cand)
	}
	field := ir.GetProviderFieldConfig(cand.CatalogCode, model).ToolResultIDField
	fixed, err := toolhistory.Repair(body, protocol, toolhistory.Options{ResultIDField: field})
	if err != nil {
		return nil, &upstream.Error{
			Kind: errorsx.KindClientBug, StatusCode: http.StatusBadRequest,
			Message: "invalid tool history", Err: err,
		}
	}
	return fixed, nil
}
