// Package admin — cross_table_request_id_representation_test.go
//
// 跨表关联 id 的**文本表示一致性**守卫（2026-10-08，§4.6.66）。
//
// 背景（D-RL-01 的实测闭合）
// ----------------------------
// 三张表记的是**同一个** request id，但列类型不同：
//
//	request_logs.request_id                 text NOT NULL   → 32-hex（无连字符）
//	request_state_transitions.request_id     text            → 32-hex（无连字符）
//	routing_decision_log.request_id         uuid NOT NULL   → 36 位带连字符
//
// PG 把 text 里那个 32-hex 灌进 uuid 列时，会**规范化成带连字符的 36 位**再存。
// 于是同一个 id 在两张表里**字符串不相等**，跨表 join / 精确匹配必然落空。
// 生产实测（只读消费端）：
//
//	routing-log 的 id 原样打 /api/admin/request-journeys/{id}   15/15 → 404
//	同一批 id 去掉连字符后再打                                  15/15 → 200
//
// ⇒ 这**不是**「两套 id 空间」，而是**同一 id 的两种文本表示**。
// 移动端 RoutingLogView.vue:117 拿端点原样返回的 id 去跳 /journey/{id}，必然 404。
//
// 本守卫的职责与形态
// ------------------
// 采用**已登记形态**（与本仓 LEGACY token 表同一思路）：登记一处**已知表示差异**，
// 断言「它仍是这样」——
//   · 今天绿：差异存在，且被显式写在这里，而不是像以前那样只躺在事故现场；
//   · 谁把 routing-log 改成输出 32-hex（修法），本守卫会红，提示把它从登记表移除；
//   · 谁让差异**扩散**到别的表或别的端点，本守卫立刻红。
//
// 一条恒绿断言毫无价值：这里断言的是「这一处投影当前是 uuid::text 未规范化」这一
// **具体事实**，§4.6.66 给了变异验证（把它改成规范化 ⇒ 本守卫转红）。
//
// 离线读源码，不连库、不发网络请求。
package admin

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// canonicalRequestIDRe 匹配 32 位无连字符 hex（text 列的规范表示）。
var canonicalRequestIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// uuidTextRe 匹配 uuid 列经 ::text 后的带连字符表示。
var uuidTextRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// readRLSource 读 admin 包内源码。
// 刻意不叫 readAdminSource —— 同包的 session_bodies_pairing_test.go 已占用该名，
// 重复声明会 build failed（加 helper 前先 grep 包内是否已有）。
func readRLSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestCrossTableRequestIDRepresentation pins the known divergence between the
// three tables that carry the same request id.
func TestCrossTableRequestIDRepresentation(t *testing.T) {
	t.Run("schema: 三张表的列类型是已知的两种", func(t *testing.T) {
		// 列类型事实取自 sql/schema/01-schema.sql（DDL 单一事实源）。
		// 这里断言的是「本守卫依赖的类型事实仍然成立」——
		// 若有人把 routing_decision_log.request_id 改成 text，本子测试会红，
		// 提醒本守卫的登记表可能已经过期。
		schema, err := os.ReadFile("../sql/schema/01-schema.sql")
		if err != nil {
			t.Skipf("读不到 schema 快照（%v），跳过类型事实断言", err)
		}
		s := string(schema)
		if !strings.Contains(s, "request_id uuid NOT NULL,") {
			t.Error("schema 里已找不到 `request_id uuid NOT NULL,`：" +
				"若 routing_decision_log.request_id 已改为 text，" +
				"请把本守卫的登记表与 §4.6.66 一并更新")
		}
		if !strings.Contains(s, "request_id text NOT NULL,") {
			t.Error("schema 里已找不到 `request_id text NOT NULL,`：" +
				"request_logs 侧的列类型变了，请复核跨表表示一致性")
		}
	})

	t.Run("已登记：routing-log 端点原样输出 uuid::text（未规范化）", func(t *testing.T) {
		src := readRLSource(t, "credential_routing_log.go")

		// 该端点把 routing_decision_log.request_id 直接 ::text 选出。
		raw := regexp.MustCompile(`rdl\.request_id::text AS request_id`)
		if !raw.MatchString(src) {
			t.Fatal("未找到 `rdl.request_id::text AS request_id`：" +
				"routing-log 端点的 request_id 投影已改。" +
				"若已改成规范化（replace(...::text,'-','')）请把本登记项删除；" +
				"若是别的改法，请更新 §4.6.66 并确认移动端 /journey 跳转已通")
		}

		// 反向断言：此刻**不得**已经出现规范化表达式，
		// 否则本登记项已过期（否则它会变成一条恒绿断言）。
		normalized := regexp.MustCompile(`replace\(\s*rdl\.request_id::text\s*,\s*'-'\s*,\s*''\s*\)`)
		if normalized.MatchString(src) {
			t.Error("routing-log 已改为输出规范化 32-hex —— " +
				"本守卫的登记项已过期，请删除它（否则它是一条恒绿断言）")
		}

		// 形态自证：确认这两种表示确实互不相等，
		// 免得「差异」建立在错误的正则上（量具先自证）。
		uuidForm, textForm := "249b28ac-4bd2-a898-a44c-9365343f4045", "249b28ac4bd2a898a44c9365343f4045"
		if !uuidTextRe.MatchString(uuidForm) {
			t.Fatalf("量具自证失败：%q 不被识别为 uuid 形式", uuidForm)
		}
		if !canonicalRequestIDRe.MatchString(textForm) {
			t.Fatalf("量具自证失败：%q 不被识别为 32-hex 形式", textForm)
		}
		if strings.ReplaceAll(uuidForm, "-", "") != textForm {
			t.Fatal("量具自证失败：两种形式的规范化结果不相等")
		}
	})

	t.Run("扩散防护：已登记的两处之外不得有第三个未规范化投影", func(t *testing.T) {
		// 已登记的未规范化点（§4.6.66 实测确认，两处同源同表）：
		//   admin/credential_routing_log.go → /api/credentials/routing-log
		//   admin/routing.go                 → /api/routing/decisions
		// 任何**新增**的 `…request_id::text AS request_id` 都是把同一缺陷复制一份。
		registered := map[string]bool{
			"credential_routing_log.go": true,
			"routing.go":                 true,
		}
		entries, err := os.ReadDir(".")
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		pat := regexp.MustCompile(`\w+\.request_id::text AS request_id`)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			b, err := os.ReadFile(name)
			if err != nil {
				continue
			}
			for i, line := range strings.Split(string(b), "\n") {
				if !pat.MatchString(line) {
					continue
				}
				if registered[name] {
					continue
				}
				t.Errorf("%s:%d 出现未登记的 `…request_id::text AS request_id`："+
					"uuid 列的 ::text 会带连字符、text 列不带，跨表必然 join 不通。"+
					"请用 replace(x::text,'-','') 规范化；"+
					"若确认也是同一缺陷，请登记进本表并更新 §4.6.66；"+
					"已登记的两处：credential_routing_log.go（/api/credentials/routing-log）、"+
					"routing.go（/api/routing/decisions）",
					name, i+1)
			}
		}

		// 反向自检：登记表里的每一项都**必须真的存在**。
		// 否则有人把某处改好之后忘了摘登记，它会退化成一条恒绿断言。
		for name := range registered {
			b, err := os.ReadFile(name)
			if err != nil {
				t.Errorf("登记项 %s 已不存在：请从本表移除", name)
				continue
			}
			if !pat.MatchString(string(b)) {
				t.Errorf("登记项 %s 里已找不到 `…request_id::text AS request_id`："+
					"该处可能已修复，请把它从登记表移除（否则它恒绿、毫无牙）", name)
			}
		}
	})
}
