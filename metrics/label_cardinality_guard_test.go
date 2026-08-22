package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// collectDeclaredDescs 从默认注册表收集所有已注册 Desc（不依赖是否有样本）。
// PrometheusRecorder 的字段都是未导出的，反射读不到；Registry.Describe(ch)
// 是稳定导出途径：它把每个 collector 的 Desc 发到 channel，无论是否有人
// 调过 WithLabelValues。
func collectDeclaredDescs(t *testing.T) []string {
	t.Helper()
	reg, ok := prometheus.DefaultRegisterer.(*prometheus.Registry)
	if !ok {
		t.Fatalf("DefaultRegisterer is %T, not *Registry", prometheus.DefaultRegisterer)
	}
	ch := make(chan *prometheus.Desc, 256)
	go func() {
		reg.Describe(ch)
		close(ch)
	}()
	var out []string
	for d := range ch {
		out = append(out, d.String())
	}
	return out
}

// parseVarLabels 解析 *prometheus.Desc.String() 里的 variableLabels。
// Desc.String() 形如（prometheus client_golang 实际格式，已用 probe 验证）:
//
//	Desc{fqName: "x", help: "h", constLabels: {}, variableLabels: {provider_id,foo}}
//
// 无 label 时为 variableLabels: {}。variableLabels 字段未导出，
// Desc.String() 是唯一稳定导出途径。
func parseVarLabels(descStr string) []string {
	i := strings.Index(descStr, "variableLabels: {")
	if i < 0 {
		return nil
	}
	rest := descStr[i+len("variableLabels: {"):]
	j := strings.Index(rest, "}")
	if j < 0 {
		return nil
	}
	inner := strings.TrimSpace(rest[:j])
	if inner == "" {
		return nil
	}
	out := []string{}
	for _, p := range strings.Split(inner, ",") {
		p = strings.TrimSpace(p)
		// 约束 label 形如 c(provider_id)；剥掉外层。
		p = strings.TrimSuffix(strings.TrimPrefix(p, "c("), ")")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// TestNoHighCardinalityLabels 是 GW-00 的低基数防回归门禁。
//
// 它从默认注册表收集所有已注册 Desc（Registry.Describe，不依赖是否有样本），
// 解析每个 metric 声明的 variable label，断言不出现 README §5 Phase 0 /
// 02-CROSS-REPO-EVENT-CONTRACT.md §3 明令禁止的高基数或高敏维度。
//
// 用 Registry.Describe 而非 Gather：带 label 的 Vec 在第一次 WithLabelValues
// 之前不会产出样本，Gather 看不到它的 label 名；Describe 总会发出声明的 Desc。
// PrometheusRecorder 字段都是未导出的，反射读不到，注册表是唯一稳定途径。
//
// 禁止 label（按风险分类）：
//   - request_id / session_id / turn_no / correlation_id / idempotency_key
//     （每请求一个值，基数无限）
//   - credential_id / credential_label（每 credential 一个值，随部署无限增长）
//   - tenant_id（signed tenant 不得作为 label 维度）
//   - user_id / identity_hash（用户维度，高基数 + PII 风险）
//   - prompt / token / api_key / secret / bearer（高敏正文/凭据）
//   - model（模型名空间大；provider 已是低维度的等价分组）
func TestNoHighCardinalityLabels(t *testing.T) {
	descs := collectDeclaredDescs(t)
	if len(descs) == 0 {
		t.Fatal("no Descs collected from default registry; is PrometheusRecorder registered?")
	}

	forbiddenExact := map[string]string{
		"request_id":       "high-cardinality (per-request)",
		"session_id":       "high-cardinality (per-session)",
		"turn_no":          "high-cardinality (per-turn)",
		"correlation_id":   "high-cardinality (per-request)",
		"idempotency_key":  "high-cardinality (per-request)",
		"credential_id":    "high-cardinality (per-credential)",
		"credential_label": "high-cardinality (per-credential)",
		"tenant_id":        "signed tenant must not be a label dimension",
		"user_id":          "high-cardinality + PII risk",
		"identity_hash":    "high-cardinality (per-user)",
		"prompt":           "prompt content must not be a label",
		"token":            "credential token must not be a label",
		"api_key":          "api key must not be a label",
		"secret":           "secret must not be a label",
		"bearer":           "bearer token must not be a label",
		"model":            "model name space is large; use 'provider' instead",
	}

	// 低敏子串允许的 label 白名单（含 'id'/'name' 但合法的）。
	sensitiveFragmentAllowlist := map[string]bool{
		"rule_id": true, "rule_name": true, "pool_id": true,
		"provider_id": true,
	}

	labelToDescs := map[string][]string{} // label -> desc strings using it
	var violations []string
	providerIDSeen := false

	for _, ds := range descs {
		fqName := parseFQName(ds)
		for _, label := range parseVarLabels(ds) {
			labelToDescs[label] = append(labelToDescs[label], fqName)
			if label == "provider_id" {
				providerIDSeen = true
			}
			if reason, bad := forbiddenExact[label]; bad {
				violations = append(violations,
					"metric "+fqName+" declares forbidden label "+label+
						" ("+reason+")")
				continue
			}
			lower := strings.ToLower(label)
			for frag := range map[string]struct{}{
				"secret": {}, "password": {}, "token": {}, "prompt": {}, "bearer": {},
			} {
				if strings.Contains(lower, frag) && !sensitiveFragmentAllowlist[label] {
					violations = append(violations,
						"metric "+fqName+" label "+label+" contains sensitive fragment '"+frag+"'")
				}
			}
		}
	}

	if len(violations) > 0 {
		t.Errorf("high-cardinality/sensitive label violations:\n  %s",
			strings.Join(violations, "\n  "))
	}
	if !providerIDSeen {
		t.Errorf("expected low-cardinality 'provider_id' label to be present after GW-00 migration; labels seen: %v", labelKeys(labelToDescs))
	}
}

// parseFQName 从 Desc.String() 抽 fqName，用于违规报错定位。
func parseFQName(descStr string) string {
	i := strings.Index(descStr, "fqName: \"")
	if i < 0 {
		return "?"
	}
	rest := descStr[i+len("fqName: \""):]
	if j := strings.Index(rest, "\""); j >= 0 {
		return rest[:j]
	}
	return "?"
}

func labelKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestDispatchGovernorLabelAllowlist_StageCEnforced
//
// ADR-0003 §"Stage C — Metrics & snapshot pipeline" — flips the Stage A
// scaffold from t.Skip-gated placeholder to a hard enforcement. For every
// metric fqName in domains/dispatch/governor_metrics.go (fq prefix
// "dispatch_governor_"), the test asserts:
//
//   - the metric's variableLabels are ⊆ {backend, mode, result, state};
//   - any emitted series with one of those labels uses only the
//     values declared in the matching closed-enum allowlist.
//
// Drift between the allowlist tables below and the constants in
// domains/dispatch (governor_backend.go, queued_request.go,
// governor_snapshot.go, governor_metrics.go) is a coordinate break for
// downstream dashboards/alerts. Stage C flips enforcement; Stage D/E
// may extend the table.
func TestDispatchGovernorLabelAllowlist_StageCEnforced(t *testing.T) {
	// Closed-enum value tables. Mirror the constants in:
	//   - domains/dispatch/governor_backend.go       (backendValues)
	//   - domains/dispatch/queued_request.go         (modeValues)
	//   - domains/dispatch/governor_snapshot.go      (stateValues)
	//   - domains/dispatch/governor_metrics.go       (resultValues)
	wantBackends := []string{"local", "redis_enforce", "redis_shadow"}
	wantModes := []string{"concurrency", "rpm", "tpm", "disabled"}
	wantResults := []string{"ready", "saturated", "invalid_token", "unknown"}
	wantStates := []string{"ready", "queue_full", "governor_saturated", "unknown"}

	allowedLabels := map[string]bool{
		"backend": true,
		"mode":    true,
		"result":  true,
		"state":   true,
	}
	allowedValues := map[string]map[string]bool{
		"backend": setFrom(wantBackends),
		"mode":    setFrom(wantModes),
		"result":  setFrom(wantResults),
		"state":   setFrom(wantStates),
	}

	descs := collectDeclaredDescs(t)
	var governorDescs []string
	for _, ds := range descs {
		fqName := parseFQName(ds)
		if !strings.HasPrefix(fqName, "dispatch_governor_") {
			continue
		}
		governorDescs = append(governorDescs, ds)
		labels := parseVarLabels(ds)
		for _, l := range labels {
			if !allowedLabels[l] {
				t.Errorf("dispatch_governor_* metric %q declares non-allowlist label %q (allowed: backend/mode/result/state)", fqName, l)
			}
		}
	}
	// If no dispatch_governor_* descriptors are registered, the dispatch
	// package was not imported by this test run. Skip — the test enforces
	// only when the metrics package's test binary pulls dispatch in
	// transitively (via `go test ./...`) or directly via the blank import
	// in domains/dispatch/governor_metrics_test.go.
	if len(governorDescs) == 0 {
		t.Skip("no dispatch_governor_* descriptors in this test binary; run via `go test ./...` (or `./domains/dispatch/`) to register the dispatch package")
	}

	// Gather actual series to verify emitted label values stay inside
	// the allowlist. (Describe reports declared labels even when the
	// vec has no observed children — Gather requires WithLabelValues to
	// have been called at least once.)
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), "dispatch_governor_") {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				lname := lp.GetName()
				lval := lp.GetValue()
				if !allowedLabels[lname] {
					continue // already reported above
				}
				if vset, ok := allowedValues[lname]; ok && !vset[lval] {
					t.Errorf("dispatch_governor_* metric %q emitted off-list label value %s=%q (allowed: %v)",
						mf.GetName(), lname, lval, sortedKeys(vset))
				}
			}
		}
	}
}

func setFrom(xs []string) map[string]bool {
	out := make(map[string]bool, len(xs))
	for _, x := range xs {
		out[x] = true
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Stable enough for test error messages; not a sort guarantee.
	return keys
}
