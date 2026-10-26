package admin

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// sanitizeMatchedRule is a read-only rule snapshot for hit-review cards.
// It never includes request plaintext — only the published rule definition.
type sanitizeMatchedRule struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Type     string                 `json:"type"`
	Kind     string                 `json:"kind"`
	Pattern  string                 `json:"pattern"`
	Group    int                    `json:"group"`
	Scopes   []string               `json:"scopes"`
	Action   string                 `json:"action"`
	Enabled  bool                   `json:"enabled"`
	Priority int                    `json:"priority"`
	TenantID string                 `json:"tenant_id,omitempty"`
	Samples  []sanitizerules.Sample `json:"samples,omitempty"`
	Missing  bool                   `json:"missing,omitempty"`
}

type sanitizeRuleEventView struct {
	sanitizerules.Event
	MatchedRule *sanitizeMatchedRule `json:"matched_rule,omitempty"`
}

const matchedRuleSampleCap = 8

func enrichSanitizeRuleEvents(ctx context.Context, store sanitizerules.Store, events []sanitizerules.Event) []sanitizeRuleEventView {
	out := make([]sanitizeRuleEventView, 0, len(events))
	if store == nil || len(events) == 0 {
		for _, e := range events {
			out = append(out, sanitizeRuleEventView{Event: e})
		}
		return out
	}
	versions := map[int64]sanitizerules.Version{}
	for _, e := range events {
		view := sanitizeRuleEventView{Event: e}
		if e.Version < 1 || e.RuleID == "" {
			out = append(out, view)
			continue
		}
		ver, ok := versions[e.Version]
		if !ok {
			loaded, err := store.GetVersion(ctx, e.Version)
			if err != nil {
				view.MatchedRule = &sanitizeMatchedRule{ID: e.RuleID, Missing: true}
				out = append(out, view)
				continue
			}
			versions[e.Version] = loaded
			ver = loaded
		}
		view.MatchedRule = matchedRuleFromVersion(ver, e.RuleID)
		out = append(out, view)
	}
	return out
}

func matchedRuleFromVersion(ver sanitizerules.Version, ruleID string) *sanitizeMatchedRule {
	for _, r := range ver.Config.Rules {
		if r.ID != ruleID {
			continue
		}
		samples := r.Samples
		if len(samples) > matchedRuleSampleCap {
			samples = samples[:matchedRuleSampleCap]
		}
		return &sanitizeMatchedRule{
			ID: r.ID, Name: r.Name, Type: r.Type, Kind: r.Kind,
			Pattern: r.Pattern, Group: r.Group, Scopes: append([]string(nil), r.Scopes...),
			Action: r.Action, Enabled: r.Enabled, Priority: r.Priority,
			TenantID: r.TenantID, Samples: samples,
		}
	}
	return &sanitizeMatchedRule{ID: ruleID, Missing: true}
}
