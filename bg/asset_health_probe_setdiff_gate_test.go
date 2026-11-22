package bg

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/apihub"
)

// ── 门 8：源表查询失败 ⇒ 绝不能把整张表打成 down ──────────────────────
//
// 2026-11-23（L4）修掉的隐患：旧代码 `liveLLMs, _ := p.syncer.LLMEndpoints(ctx)`
// **吞掉了错误**。查询一失败 ⇒ liveLookup 为空 ⇒ 旧逻辑把「查不到」当成
// 「源表空了」⇒ 该租户的**全部**资产被一次性标成 down（静默灾，且不可逆：
// down 是终态）。
//
// 新逻辑：查询失败 ⇒ 跳过该 kind，一个字都不改。
//
// ★ 这条门是「失败语义优先于数据」：门断言的不是「标了 0 行」，
//
//	而是「失败时**没有调用**任何标记方法」——后者才是真正的保证。
func TestProbeSkipsDownMarkingWhenLiveSourceQueryFails(t *testing.T) {
	all := []apihub.Asset{
		asset(1, "t1", apihub.HealthUnknown),
		asset(2, "t1", apihub.HealthUnknown),
		asset(3, "t1", apihub.HealthUnknown),
	}
	st := &probeStore{tenants: []string{"t1"}, all: all}

	p := NewAssetHealthProbe(apihub.New(st), failingSyncer{})
	d, r, err := p.ProbeOnce(context.Background())
	if err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}
	if d != 0 || r != 0 {
		t.Errorf("degraded=%d removed=%d，期望都是 0（源表查询失败 ⇒ 不做任何标记）", d, r)
	}
	if len(st.missingDownCalls) != 0 {
		t.Errorf("MarkMissingDown 被调用 %d 次，期望 0：%+v",
			len(st.missingDownCalls), st.missingDownCalls)
	}
}

// failingSyncer 的两个源表查询都失败。
type failingSyncer struct{}

func (failingSyncer) LLMEndpoints(context.Context) ([]apihub.Asset, error) {
	return nil, errors.New("synthetic source query failure")
}
func (failingSyncer) MCPServers(context.Context) ([]apihub.Asset, error) {
	return nil, errors.New("synthetic source query failure")
}

// ── 门 9：旧实现的「上界告警」判据是反的，必须已经消失 ────────────────
//
// 旧代码：
//
//	for page := 0; page < maxPages; page++ { ... }
//	if offset+pageSize <= maxPages*pageSize {
//	    slog.Warn("asset health: 分页到达上界，资产表可能超过 10000 行", ...)
//	}
//
// 判据是**反的**：正常跑完（提前 break，offset < 上界）时条件成立 ⇒ 每轮都喊；
// 真撞上界（offset 恰好 == 上界）时条件不成立 ⇒ 反而不喊。
// ⇒ 211 上那条 WARN 从来不是「表太大」，而是判据写反了
//
//	（2026-11-23 实测：default 租户只有 1420 行资产）。
//
// 现在整个分页 + 上界告警都被集合差取代 ⇒ 这条门钉住「旧形态不得复活」。
func TestProbeNoLongerCarriesPaginationCeilingWarning(t *testing.T) {
	src, err := os.ReadFile("asset_health_probe.go")
	if err != nil {
		t.Fatalf("读 asset_health_probe.go 失败: %v", err)
	}
	// ★ 只扫**代码**：注释里引用旧实现（"maxPages" 出现在解释旧判据的注释里）
	//   是正常的，扫注释会把「记录历史」也判成违规。
	body := stripGoComments(string(src))
	for _, banned := range []string{
		"分页到达上界",
		"maxPages",
		"apihub.Filter{Limit: pageSize, Offset: offset}",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("asset_health_probe.go 的代码里还有 %q。\n"+
				"  ★ 集合差口径下没有分页，也就没有「上界」这个概念。\n"+
				"  ★ 旧的上界告警判据还是反的（正常跑完就喊，真撞上界不喊）——\n"+
				"    复活它等于把一条假告警放回生产日志。", banned)
		}
	}
}

// stripGoComments 去掉 // 行注释与 /* */ 块注释，只留代码。
func stripGoComments(src string) string {
	var out strings.Builder
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		if inBlock {
			if idx := strings.Index(line, "*/"); idx >= 0 {
				line = line[idx+2:]
				inBlock = false
			} else {
				continue
			}
		}
		for {
			idx := strings.Index(line, "//")
			if idx < 0 {
				break
			}
			// 字符串字面量里的 "//"（如 "http://"）不是注释。
			prefix := line[:idx]
			if strings.Count(prefix, `"`)%2 == 1 {
				break
			}
			line = line[:idx]
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}
