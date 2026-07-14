package boardcache

import (
	"net/url"
	"strconv"
	"strings"
)

const (
	fieldReq        = "s:requests"
	fieldSuccess    = "s:success"
	fieldFailure    = "s:failure"
	fieldPrompt     = "s:prompt_tokens"
	fieldCompletion = "s:completion_tokens"
	fieldTotalTok   = "s:total_tokens"
	fieldCredits    = "s:credits"
	fieldLatency    = "s:latency_ms_sum"
	fieldCostUSD    = "s:cost_usd"
)

func dimField(dimType, dimKey, metric string) string {
	return "d:" + dimType + ":" + escapeDimKey(dimKey) + ":" + metric
}

func escapeDimKey(key string) string {
	return url.PathEscape(key)
}

func unescapeDimKey(key string) string {
	out, err := url.PathUnescape(key)
	if err != nil {
		return key
	}
	return out
}

func parseDimField(field string) (dimType, dimKey, metric string, ok bool) {
	if !strings.HasPrefix(field, "d:") {
		return "", "", "", false
	}
	parts := strings.Split(field, ":")
	if len(parts) < 4 {
		return "", "", "", false
	}
	dimType = parts[1]
	metric = parts[len(parts)-1]
	dimKey = unescapeDimKey(strings.Join(parts[2:len(parts)-1], ":"))
	return dimType, dimKey, metric, true
}

type summaryCounters struct {
	Requests         int64
	Success          int64
	Failure          int64
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	Credits          int64
	LatencyMsSum     int64
	CostUSD          float64
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func parseFloat64(s string) float64 {
	n, _ := strconv.ParseFloat(s, 64)
	return n
}

var dimPieKey = map[string]string{
	"client_profile": "clients",
	"virtual_ip":     "virtual_ips",
	"identity_hash":  "identity_hashes",
	"model":          "models",
	"error_kind":     "errors",
	"tenant":         "tenants",
	"provider":       "providers",
}
