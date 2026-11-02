package bg

// credibility_trusted_skip.go — 信任名单：自动扫描（定时 / 探活成功 / integrity
// 异常）跳过；★ 手动 ScanNode（admin「立即检测」）不受限。
//
// 条目形态（大小写不敏感，空白 trim）：
//   · `model`                 — 任意供应商下的该 raw_model
//   · `provider/model`         — catalog_code（或 provider code）+ raw_model
//   · `provider:model`         — 同上（冒号写法）
//
// 配置：model_credibility.trusted_skip（JSON 字符串数组，默认 []）。

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// loadTrustedSkipEntries reads the platform setting. Malformed → empty
// (fail open to scanning rather than silently skipping everything).
func loadTrustedSkipEntries() []string {
	if settings.Global == nil {
		return nil
	}
	sp := settings.Global.Spec("model_credibility.trusted_skip")
	if sp == nil {
		return nil
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, "model_credibility.trusted_skip", "")
	if err != nil || len(raw) == 0 {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return normalizeTrustedSkip(arr)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" || s == "[]" {
			return nil
		}
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return normalizeTrustedSkip(arr)
		}
		// Single bare entry stored as a plain string.
		return normalizeTrustedSkip([]string{s})
	}
	slog.Warn("model credibility: trusted_skip parse failed; ignoring",
		"error", err, "raw_len", len(raw))
	return nil
}

func normalizeTrustedSkip(in []string) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// isTrustedSkip reports whether (providerCode, rawModel) matches the trusted
// skip list. providerCode may be empty — then only bare-model entries match.
func isTrustedSkip(providerCode, rawModel string, entries []string) bool {
	model := strings.ToLower(strings.TrimSpace(rawModel))
	if model == "" || len(entries) == 0 {
		return false
	}
	prov := strings.ToLower(strings.TrimSpace(providerCode))
	for _, e := range entries {
		if e == model {
			return true
		}
		var sep string
		if i := strings.IndexAny(e, "/:"); i >= 0 {
			sep = e[i : i+1]
		}
		if sep == "" {
			continue
		}
		parts := strings.SplitN(e, sep, 2)
		if len(parts) != 2 {
			continue
		}
		p, m := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if p == "" || m == "" || m != model {
			continue
		}
		// provider/model entries require a known provider code.
		if prov == "" {
			continue
		}
		if p == prov {
			return true
		}
	}
	return false
}

func (w *ModelCredibilityWorker) trustedSkip(providerCode, rawModel string) bool {
	_ = w
	return isTrustedSkip(providerCode, rawModel, loadTrustedSkipEntries())
}
