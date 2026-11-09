// reasoning_budget.go (2026-11-10) — reasoning 模型烧光输出预算时的
// max_tokens 加倍助手。策略见 executor_chat.go 的 reasoningBudgetBumped
// 注释：一次免费同候选重试，二次仍烧完走既有 empty failover。
package executors

import (
	"encoding/json"
)

// bumpReasoningBudget doubles the outbound max_tokens (min 2048, cap 32768)
// so a reasoning model with finish_reason=length has room to emit visible
// output after thinking. Returns the mutated body and whether a bump happened.
// Bodies without an explicit output cap are left untouched — those flows are
// owned by the stream-continuation machinery.
func bumpReasoningBudget(body []byte) ([]byte, bool) {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return body, false
	}
	field := "max_tokens"
	if _, ok := root["max_tokens"]; !ok {
		if _, ok2 := root["max_completion_tokens"]; ok2 {
			field = "max_completion_tokens"
		} else {
			return body, false
		}
	}
	var cur float64
	if json.Unmarshal(root[field], &cur) != nil || cur <= 0 {
		return body, false
	}
	next := cur * 2
	if next < 2048 {
		next = 2048
	}
	if next > 32768 {
		next = 32768
	}
	if int64(next) == int64(cur) {
		return body, false
	}
	nb, err := json.Marshal(int64(next))
	if err != nil {
		return body, false
	}
	root[field] = nb
	out, err := json.Marshal(root)
	if err != nil {
		return body, false
	}
	return out, true
}
