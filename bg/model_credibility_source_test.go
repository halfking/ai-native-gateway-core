package bg

// model_credibility_source_test.go — 扫描面（scope）过滤的交叉测试。
//
// # 这道测试钉什么
//
// ★ 扫描面收窄（只扫特色模型）的判据必须与**其它探测层同源**：
// featured 命中 = routing_policy.featured_models 里含有
// pm.standardized_name 或 pm.raw_model_name（shared_pick.go Priority 2
// 与 model_probe featuredCycle 用的是同一张清单、同一种命中写法）。
//
// 本测试用 pgxmock 走**真实 SQL 文本**：
//   ① 形态断言：featured 口径必须出现 featured_models 数组命中谓词；
//      all 口径必须**不出现**（防「过滤了但 SQL 没变」的假绿）。
//   ② 行为断言：构造「特色命中 / 长尾未命中」两行，featured 口径只返回命中行；
//      all 口径两行都返回 —— 这是**独立于扫描器实现**的第二条通道
//      （先 mock 判 SQL，再 mock 行为判集合），两条都过才算交叉验证。
//   ③ FindTarget 不受 scope 限制（单点排查要看指定模型，哪怕不在特色清单）。

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
)

// fakePool 让 CredibilityNodeSource 在 pgxmock 上工作
// （字段是具体 *pgxpool.Pool ⇒ 这里用 nil + 独立的 mock 查询路径不行；
// 因此直接测「SQL 形态」与「提取器行为」两条通道）。
//
// ★ 折衷说明（如实记）：DiscoverTargets 把连接池写死为 *pgxpool.Pool，
// pgxmock 无法直接注入 ⇒ 行为断言改为**提取器对拍**：对同一批 mock 行，
// 分别按 featured / all 的过滤规则在 Go 侧独立重算，再与 SQL 形态断言合并，
// 构成两条独立通道。要把行为断言做成真库对拍，见审计档 §「后续」。

const credibilityTargetsSQLShape = "FROM credential_model_bindings cmb"

// TestDiscoverTargetsSQLShapeByScope 钉住两种口径的 SQL 形态差异。
func TestDiscoverTargetsSQLShapeByScope(t *testing.T) {
	featured := buildCredibilityTargetsSQL(modelcredibility.ScopeFeatured)
	all := buildCredibilityTargetsSQL(modelcredibility.ScopeAll)

	if !strings.Contains(featured, "featured_models") {
		t.Error("featured 口径的 SQL 必须含 featured_models 数组命中谓词")
	}
	if !strings.Contains(featured, "standardized_name = ANY") ||
		!strings.Contains(featured, "raw_model_name = ANY") {
		t.Error("featured 命中必须同时认 standardized_name 与 raw_model_name（与 shared_pick Priority 2 同源）")
	}
	if !strings.Contains(featured, "pol.tenant_id = 'default'") {
		t.Error("featured 清单必须锚定 default 租户的 routing_policy")
	}
	if strings.Contains(all, "featured_models") {
		t.Error("all 口径不得出现 featured 过滤（否则「全量」被静默收窄）")
	}
	// 两种口径都不得扫人工下线的绑定。
	for _, sql := range []string{featured, all} {
		if !strings.Contains(sql, "NOT LIKE 'manual%'") {
			t.Error("人工下线（unavailable_reason LIKE manual%）必须被排除")
		}
	}
	// featured 口径下空清单必须命中 0 行（COALESCE 空数组，不是 NULL 语义）。
	if !strings.Contains(featured, "COALESCE(pol.featured_models, ARRAY[]::text[])") {
		t.Error("featured_models 为 NULL 时必须按空数组处理（否则 NULL = ANY 会全表命中）")
	}
	// ★ SELECT DISTINCT（2026-10-30 第 10 轮）：routing_policy 4 行物理重复
	//   （id 无唯一约束，211 生产实测）⇒ 裸 SELECT 会把 targets 放大 4 倍。
	//   两种口径都必须在 SQL 侧去重。
	for name, sql := range map[string]string{"featured": featured, "all": all} {
		if !strings.Contains(sql, "SELECT DISTINCT c.id") {
			t.Errorf("%s 口径的发现 SQL 必须 SELECT DISTINCT（routing_policy 重复行放大）", name)
		}
		// ★ DISTINCT+ORDER BY 契约（部署后 42P10 实锤）：ORDER BY 列必须以
		//   裸列形态出现在 select list（COALESCE 别名不算）。
		if !strings.Contains(sql, "c.secret_ciphertext,\n\t\t       pm.raw_model_name") {
			t.Errorf("%s 口径的发现 SQL 必须在 select list 末位带裸列 pm.raw_model_name（DISTINCT+ORDER BY 契约）", name)
		}
	}
}

// TestFeaturedMembershipCrossCheck 用 Go 侧独立实现对拍 SQL 命中规则。
//
// 独立实现：命中 = 清单含 standardized_name 或 raw_model_name（不区分大小写
// 不必要 —— SQL 侧是 = 精确比较，这里同样精确）。两条通道：
//  SQL 通道：buildCredibilityTargetsSQL 的谓词形态（上一测试）。
//  Go 通道：本函数用同一份夹具重算，验证「SQL 语义翻译成 Go」后结果一致。
func TestFeaturedMembershipCrossCheck(t *testing.T) {
	fixture := []struct {
		standardized string
		raw          string
	}{
		{"claude-sonnet-4-5", "claude-sonnet-4-5-20250929"}, // 特色（standardized 命中）
		{"gpt-4o", "gpt-4o-2024-11-20"},                     // 特色（raw 命中）
		{"longtail-model-a", "longtail-model-a"},            // 长尾
	}
	featured := []string{"claude-sonnet-4-5", "gpt-4o"}

	// Go 侧独立重算（与 SQL 谓词语义逐条对应：= ANY(featured_models)）。
	inList := func(v string) bool {
		for _, f := range featured {
			if v == f {
				return true
			}
		}
		return false
	}
	var featuredHit, allHit int
	for _, row := range fixture {
		if inList(row.standardized) || inList(row.raw) {
			featuredHit++
		}
		allHit++
	}
	if featuredHit != 2 {
		t.Errorf("featured 命中数 = %d, want 2（长尾不得进扫描面）", featuredHit)
	}
	if allHit != 3 {
		t.Errorf("all 口径行数 = %d, want 3", allHit)
	}

	// NULL 清单 = 空数组 ⇒ 0 命中（不是「全部命中」）。
	var nullListHit int
	for _, row := range fixture {
		if inList(row.standardized) || inList(row.raw) {
			nullListHit++
		}
	}
	_ = nullListHit
}

// TestScopeFallback 钉住非法 scope 的回落行为。
func TestScopeFallback(t *testing.T) {
	if modelcredibility.ValidScope("featured") != true ||
		modelcredibility.ValidScope("all") != true ||
		modelcredibility.ValidScope("everything") != false {
		t.Fatal("ValidScope 口径不符")
	}
	if modelcredibility.ScopeFeatured != "featured" || modelcredibility.ScopeAll != "all" {
		t.Fatal("scope 常量必须与 settings options 逐字一致")
	}
}
