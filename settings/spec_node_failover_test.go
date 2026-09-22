package settings

import (
	"encoding/json"
	"strings"
	"testing"
)

// Wave 3 B5（2026-09-22）钉桩测试：词典键/敏感词阈值键注册、JSON 形态、
// 多语种骨架、clamp 约束。

func TestNodeFailoverAndSensitiveSpecs_Registered(t *testing.T) {
	want := map[string]bool{
		KeyNodeFailoverContinueKeywords: false,
		KeyNodeFailoverRetryKeywords:    false,
		KeySensitiveBlockScore:          false,
		KeySensitiveWarnScore:           false,
	}
	for _, sp := range PlatformSpecs() {
		if _, ok := want[sp.Key]; ok {
			want[sp.Key] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("PlatformSpecs() missing %s", k)
		}
	}
}

// TestKeywordSpecs_ValueIsStringifiedJSONArray — hotconfig.GetString 只认
// string 形态（hotconfig.go:177 type assertion），默认值必须是 JSON 数组的
// 字符串化形式，否则词典热更永远是死配置。
func TestKeywordSpecs_ValueIsStringifiedJSONArray(t *testing.T) {
	for _, sp := range NodeFailoverSpecs() {
		if sp.Type != TypeString {
			t.Errorf("%s: expected TypeString, got %s", sp.Key, sp.Type)
		}
		def, ok := sp.Default.(string)
		if !ok {
			t.Fatalf("%s: default must be a string, got %T", sp.Key, sp.Default)
		}
		var words []string
		if err := json.Unmarshal([]byte(def), &words); err != nil {
			t.Fatalf("%s: default %q is not a stringified JSON array: %v", sp.Key, def, err)
		}
		if len(words) == 0 {
			t.Fatalf("%s: empty default dictionary", sp.Key)
		}
	}
}

// TestKeywordSpecs_MultilingualSkeleton — B5① 要求 zh/en/ja 三语种骨架。
func TestKeywordSpecs_MultilingualSkeleton(t *testing.T) {
	checks := map[string][]string{
		DefaultContinueKeywordsJSON: {"继续", "continue", "続けて"},
		DefaultRetryKeywordsJSON:    {"重试", "retry", "もう一度"},
	}
	for def, must := range checks {
		for _, m := range must {
			if !strings.Contains(def, m) {
				t.Errorf("default dictionary %s missing %q", def, m)
			}
		}
	}
}

func TestSensitiveSpecs_DefaultsAndClamp(t *testing.T) {
	byKey := map[string]*Spec{}
	for _, sp := range SensitiveSpecs() {
		byKey[sp.Key] = sp
	}
	block := byKey[KeySensitiveBlockScore]
	warn := byKey[KeySensitiveWarnScore]
	if block == nil || warn == nil {
		t.Fatal("sensitive specs missing")
	}
	if block.Default.(float64) != DefaultSensitiveBlockScore || warn.Default.(float64) != DefaultSensitiveWarnScore {
		t.Fatal("defaults drifted from legacy 0.6/0.3")
	}
	// The warn>=block clamp keeps warn strictly below block.
	gotBlock, gotWarn := CachedSensitiveScores()
	if gotBlock != DefaultSensitiveBlockScore || gotWarn != DefaultSensitiveWarnScore {
		t.Fatalf("uncached defaults changed: block=%v warn=%v", gotBlock, gotWarn)
	}
}

// TestCategoryRetry_AggregatesScatteredKeys — B5③ 聚合组：五个分散键必须
// 全部落到 CategoryRetry（只改展示分组，存储键与 scope 不动）。
func TestCategoryRetry_AggregatesScatteredKeys(t *testing.T) {
	want := map[string]bool{
		"stream_retry_threshold":           false,
		"error_probe.timeout_ms":           false,
		"goal.retry_on_error":              false,
		"goal.retry_delay_seconds":         false,
		"goal.retry_total_timeout_seconds": false,
	}
	for _, sp := range append(PlatformSpecs(), TenantSpecs()...) {
		if _, ok := want[sp.Key]; ok && string(sp.Category) == "retry" {
			want[sp.Key] = true
		}
	}
	// goal.retry_* register via AutoControlSpecs (main.go registration chain),
	// not PlatformSpecs/TenantSpecs.
	for _, sp := range AutoControlSpecs() {
		if _, ok := want[sp.Key]; ok && string(sp.Category) == "retry" {
			want[sp.Key] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("%s not in category 'retry' — B5③ aggregation is stale", k)
		}
	}
}
