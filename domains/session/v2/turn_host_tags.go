package v2

// turn_host_tags.go —— 轮次级 host / tags 的取值（迁移 885，审计 A5 缺口）。
//
// 缺口本体（2026-10-20 审计）：
//   · 全族无 host/host_name/instance_id 列 ⇒「哪台机器写的这轮」无处可查；
//   · 标签只到 session 级（session_tags，迁移 351），**轮次级无 tags**。
//
// 两个值的来源都刻意做成「**有默认、不需要上游新增生产者**」：
//
//   · host：进程身份（LLM_GATEWAY_INSTANCE_ID → hostname → "llm-gateway"，
//     internal/instanceid）。写方不缺信息，只是以前没地方放。
//   · tags：轮次自身的分类列物化成的标签数组（source_kind /
//     client_protocol / agent_type / task_type），外加一个命名空间标记。
//     这些列本来就在行里，物化成数组只是让下游「按标签过滤」不必对十来个
//     列做 OR。

import (
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/instanceid"
)

// TurnTagNamespace is the marker every turn-level tag array carries.
//
// Why a marker: turn tags and session tags (public.session_tags.tag_value)
// can end up in the same consumer. The marker makes the two distinguishable
// without a second column, and it guarantees the array is never empty —
// "no tags" and "tags not written" must not look the same.
const TurnTagNamespace = "session_turn"

// resolveTurnHost returns the value written to session_turns.host.
//
// An explicitly provided host wins (so a caller that knows better — e.g. a
// replay from another instance — can say so); otherwise the process
// identity is used. The result is never empty.
func resolveTurnHost(rec TurnRecord) string {
	if host := strings.TrimSpace(rec.Host); host != "" {
		return host
	}
	return instanceid.Resolve()
}

// resolveTurnTags returns the value written to session_turns.tags.
//
// An explicitly provided non-empty tag list wins (after trimming blanks);
// otherwise the standard labels are derived from the turn's own columns.
// The result always contains at least TurnTagNamespace.
func resolveTurnTags(rec TurnRecord) []string {
	if explicit := trimTags(rec.Tags); len(explicit) > 0 {
		return explicit
	}
	out := []string{TurnTagNamespace}
	add := func(prefix, value string) {
		if v := strings.TrimSpace(value); v != "" {
			out = append(out, prefix+v)
		}
	}
	add("src:", rec.SourceKind)
	add("proto:", rec.ClientProtocol)
	add("agent:", rec.AgentType)
	add("task:", rec.TaskType)
	return out
}

// trimTags drops blank entries and surrounding whitespace.
func trimTags(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, tag := range in {
		if trimmed := strings.TrimSpace(tag); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
