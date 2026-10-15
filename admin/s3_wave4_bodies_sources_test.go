//go:build !integration

package admin

import (
	"strings"
	"testing"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

// S3 波 4（2026-10-13 停写复审计轮）bodies 读侧迁移的形状钉桩。
//
// 本轮改动四件事（明细见 docs/audit/2026-10-13-s3-wave4-bodies-retirement-assessment.md）：
//  1. session_bodies_batch 主批量取数器固定读会话族（消费方 session_compare
//     的 turns 腿是会话族；停写后新会话正文只在 session_bodies）；
//  2. session_compare 两个解析器补 request_delta/response_delta 数组臂；
//  3. compression_sessions 整只端点会话族化（canonical turns 源 + bodies 源）；
//  4. scenario_driver 落库断言改数会话族热表（数 v1 表 delta 恒 0）。
//
// 判据纪律对位：#3「允许侧必须证前提在场」——解析器数组臂的判据同时喂
// 对象形状（v1 既有行为不得回退）；#7「N 条同构腿 count==N」——会话族
// hot∪父表双腿与 compression_sessions 的双源出现次数都按计数断言。

// s3Wave4ReadFile 是 readRepoFile（session_bodies_source_test.go，仓根解析）
// 的薄别名——两侧文件都按「admin/<name>」与「cmd/<path>」取。
func s3Wave4ReadFile(t *testing.T, rel string) string {
	t.Helper()
	parts := strings.Split(rel, "/")
	return readRepoFile(t, parts...)
}

// TestS3Wave4BatchFetcherFamilySplit 钉住「主取数器=会话族、summary 回退=v1、
// 元组变体=v1」的三分格局。会话族双腿用 count==2（判据纪律 #7：删一条腿
// 的静默回退必须红）。
func TestS3Wave4BatchFetcherFamilySplit(t *testing.T) {
	if got := strings.Count(sessionBodiesByRequestIDSQL, "session_bodies"); got != 2 {
		t.Errorf("主批量取数器必须读会话族 hot∪父表两腿（count==2），实测 %d：\n%s", got, sessionBodiesByRequestIDSQL)
	}
	if strings.Contains(sessionBodiesByRequestIDSQL, "request_logs_bodies") {
		t.Errorf("主批量取数器不得再引用 v1 bodies（回退 v1 = 停写后新会话正文静默为空）：\n%s", sessionBodiesByRequestIDSQL)
	}
	if !strings.Contains(v1BodiesByRequestIDSQL, "request_logs_bodies_with_current_month") {
		t.Errorf("summary 回退专用的 v1 变体必须留在 v1 视图（与它的 v1 turns 腿同源）：\n%s", v1BodiesByRequestIDSQL)
	}
}

// TestS3Wave4CompareParsersAcceptDeltaShapes 钉住解析器的双形状合同：
// 会话族 delta 数组（新）与 v1 对象（既有）都必须解析成功——只测一边
// 就是把「迁移」换成了「反向回退」。
func TestS3Wave4CompareParsersAcceptDeltaShapes(t *testing.T) {
	idx := 0
	msgs := parseMessagesFromBody(`[{"role":"user","content":"ping"},{"role":"assistant","content":"pong"}]`, &idx)
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[0].Content != "ping" || msgs[1].Content != "pong" {
		t.Fatalf("request_delta 数组形状必须解析出 2 条消息，实测 %+v", msgs)
	}

	idx = 0
	objMsgs := parseMessagesFromBody(`{"messages":[{"role":"user","content":"legacy v1 shape"}]}`, &idx)
	if len(objMsgs) != 1 || objMsgs[0].Content != "legacy v1 shape" {
		t.Fatalf("v1 request_body 对象形状（既有行为）必须继续解析，实测 %+v", objMsgs)
	}

	idx = 0
	resp := parseResponseMessages(`[{"role":"assistant","content":"done"}]`, &idx)
	if len(resp) != 1 || resp[0].Role != "assistant" || resp[0].Content != "done" {
		t.Fatalf("response_delta 数组形状必须解析，实测 %+v", resp)
	}

	idx = 0
	respObj := parseResponseMessages(`{"choices":[{"message":{"role":"assistant","content":"openai shape"}}]}`, &idx)
	if len(respObj) != 1 || respObj[0].Content != "openai shape" {
		t.Fatalf("v1 response_body 对象形状（既有行为）必须继续解析，实测 %+v", respObj)
	}

	// 无 content 的 delta 条目（真库实测存在：{"role":"assistant"}）不得崩、
	// 不得丢角色。
	idx = 0
	bare := parseResponseMessages(`[{"role":"assistant"}]`, &idx)
	if len(bare) != 1 || bare[0].Role != "assistant" || bare[0].Content != "" {
		t.Fatalf("无 content 的 delta 条目必须产出空正文消息，实测 %+v", bare)
	}
}

// TestS3Wave4CompressionSessionsTargetsSessionFamily 钉住 compression_sessions
// 的双源会话族化与**单趟形态**：turns 源×2（countSQL+window_turns CTE）、
// bodies 源×2；v1 字面量、旧 ->'messages' 取键、ts 等值配对、**LEFT JOIN
// LATERAL** 四个旧形态在活代码里不得出现（LATERAL 体会对每个 GROUP 行
// 重跑 canonical 全源——2612 生产实测 925k turns 规模把 10s 预算打满；
// 单趟 CTE + DISTINCT ON 才是正确形态）。注释里解释历史的文字不算，
// 判据剥离注释——与 read inventory 同一处置。
func TestS3Wave4CompressionSessionsTargetsSessionFamily(t *testing.T) {
	src := stripGoComments(s3Wave4ReadFile(t, "admin/compression_sessions.go"))
	if got := strings.Count(src, "SessionFamilyTurnsSourceSQL()"); got != 2 {
		t.Errorf("compression_sessions 的 turns 源必须出现 2 次（countSQL+window_turns），实测 %d", got)
	}
	if got := strings.Count(src, "SessionFamilyBodiesSourceSQL()"); got != 2 {
		t.Errorf("compression_sessions 的 bodies 源必须出现 2 次（countSQL+window_turns），实测 %d", got)
	}
	for _, forbidden := range []string{
		"request_logs_bodies_with_current_month",
		"request_logs_with_current_month",
		"->'messages'",
		"rb2.ts = rl2.ts",
		"LEFT JOIN LATERAL",
	} {
		if strings.Contains(src, forbidden) {
			t.Errorf("compression_sessions 活代码不得再出现 %q（旧 v1/LATERAL 形态回退）", forbidden)
		}
	}
	if !strings.Contains(src, "jsonb_typeof(rb2.request_body)") && !strings.Contains(src, "jsonb_typeof(request_body)") {
		t.Error("compression_sessions 的数组长度必须先 jsonb_typeof 判形（JSON 字面量 null 是标量，裸 jsonb_array_length 报 22023）")
	}
	if !strings.Contains(src, "DISTINCT ON (gw_session_id)") {
		t.Error("每会话最新轮必须用 DISTINCT ON 单趟取（LATERAL 重跑全源是生产规模性能缺陷）")
	}
}

// TestS3Wave4BlobGovernanceTargetsSessionFamily 钉住 blob 治理的会话臂
// （退役门登记的「DROP 前必须落地」项）。要点：读源 hot∪父表双腿、
// execute 目标 session_bodies_hot、SET 清单**不得**含增量正文列
// （request_delta/response_delta 是对话内容，清了就毁导出——v1 语义
// 连对话一起丢，会话臂刻意保留更多）。
func TestS3Wave4BlobGovernanceTargetsSessionFamily(t *testing.T) {
	src := stripGoComments(s3Wave4ReadFile(t, "admin/data_lifecycle_blobs.go"))
	if got := strings.Count(sessionBodiesGovernanceSource, "session_bodies"); got != 2 {
		t.Errorf("治理读源必须含会话族 hot∪父表两腿（count==2），实测 %d：\n%s", got, sessionBodiesGovernanceSource)
	}
	for _, forbidden := range []string{
		"request_logs_bodies_with_current_month",
		"request_logs_with_current_month",
		"request_logs_bodies_hot",
		"request_logs_hot",
	} {
		if strings.Contains(src, forbidden) {
			t.Errorf("blob 治理活代码不得再出现 %q（v1 形态回退）", forbidden)
		}
	}
	if !strings.Contains(src, "UPDATE session_bodies_hot sb") {
		t.Error("execute 的 UPDATE 目标必须是 session_bodies_hot")
	}
	for _, preserved := range []string{"SET outbound_body = NULL", "request_attachments = NULL", "response_attachments = NULL"} {
		if !strings.Contains(src, preserved) {
			t.Errorf("execute 的 SET 必须清理 %q（大对象三列）", preserved)
		}
	}
	for _, delta := range []string{"request_delta = NULL", "response_delta = NULL"} {
		if strings.Contains(src, delta) {
			t.Errorf("execute 不得把增量正文置 NULL（%q）——对话内容对导出/对比必须保留", delta)
		}
	}
	if !strings.Contains(src, "VACUUM (VERBOSE, ANALYZE) session_bodies_hot") {
		t.Error("execute 后的 VACUUM 目标必须是 session_bodies_hot")
	}
}

// TestS3Wave4QualityCorrelationsTargetsSessionFamily 钉住质量相关性分析的
// 会话族重写。同层驱动 buildBreakdownQuery（判据纪律 #1：它就是发给 PG
// 的文本）。tools 桶必须带 jsonb_typeof 守卫（22023 教训）；tools/images/
// code_block 三桶必须读 outbound_body 而非 request_body（会话臂的
// request_body 是 request_delta 增量数组，读它会静默塌桶）。
func TestS3Wave4QualityCorrelationsTargetsSessionFamily(t *testing.T) {
	for _, by := range []string{"prompt_length", "tools", "images", "code_block", "language"} {
		q, err := buildBreakdownQuery(by)
		if err != nil || q == "" {
			t.Fatalf("buildBreakdownQuery(%q) 失败：%v", by, err)
		}
		// 判据按生成文本比对（canonical/切换源是运行时拼接，比对源码文本
		// 测不到真发出去的 SQL——同层判据纪律）。
		if !strings.Contains(q, dbpkg.SessionFamilyTurnsSourceSQL()) {
			t.Errorf("[%s] turns 腿必须是 canonical 会话族源", by)
		}
		// bodies 腿经切换层：单元测试二进制里 settings 缺省 ⇒ 切换层返回
		// v1 臂（request_logs_bodies_with_current_month 是切换层自己的输出，
		// 不算本文件的 v1 字面量）；部署态 native_read=true ⇒ 会话臂。
		// 判据因此断言「取源文本 == 切换层输出」而非对 bodies 名禁词。
		if !strings.Contains(q, "LEFT JOIN "+dbpkg.SessionBodiesSourceSQL()+" rb") {
			t.Errorf("[%s] bodies 腿必须以切换层输出为 FROM 源（rb 别名）", by)
		}
		if strings.Contains(q, "FROM request_logs ") || strings.Contains(q, "JOIN request_logs ") {
			t.Errorf("[%s] turns 腿不得回退 v1 基表", by)
		}
	}
	toolsQ, _ := buildBreakdownQuery("tools")
	if !strings.Contains(toolsQ, "jsonb_typeof(COALESCE(rb.outbound_body->'tools'") {
		t.Error("tools 桶必须有 jsonb_typeof 守卫（tools 为 JSON 字面量 null 时裸 jsonb_array_length 报 22023）")
	}
	// 2612 生产实测抓的第二个缺陷：会话族 quality_score 可全 NULL（镜像不
	// 回填该列），裸 AVG 返 NULL 扫进 *float64 直接崩。四个聚合列里 latency/
	// cost 原本就有 COALESCE，quality 漏了——统一钉住。
	for _, by := range []string{"prompt_length", "tools", "images", "code_block", "language"} {
		q, _ := buildBreakdownQuery(by)
		for _, agg := range []string{"COALESCE(AVG(rl.quality_score), 0)", "COALESCE(AVG(rl.cost_usd), 0)", "COALESCE(AVG(rl.latency_ms), 0)"} {
			if !strings.Contains(q, agg) {
				t.Errorf("[%s] 聚合列必须 NULL 安全（缺 %s）——会话族列可全 NULL，裸 AVG 扫描崩", by, agg)
			}
		}
	}
	for _, by := range []string{"tools", "images", "code_block"} {
		q, _ := buildBreakdownQuery(by)
		if strings.Contains(q, "rb.request_body") {
			t.Errorf("[%s] 桶不得读 rb.request_body（会话臂=增量数组，无 tools/messages 键，静默塌桶）", by)
		}
		if !strings.Contains(q, "rb.outbound_body") {
			t.Errorf("[%s] 桶必须读 rb.outbound_body（完整上游请求对象）", by)
		}
	}
}

// TestS3Wave4ScenarioDriverCountsSessionHot 钉住压测驱动断言数会话族热表：
// 停写后数 v1 表 delta 恒 0，s22-3/s23-3 会永久 FAIL——这是本轮修掉的活缺陷。
func TestS3Wave4ScenarioDriverCountsSessionHot(t *testing.T) {
	src := stripGoComments(s3Wave4ReadFile(t, "cmd/scenario_driver/main.go"))
	for _, want := range []string{"session_bodies_hot", "session_turns_hot"} {
		if !strings.Contains(src, want) {
			t.Errorf("scenario_driver 落库断言必须数 %s（S4 停写后 v1 表不再进新行）", want)
		}
	}
	for _, forbidden := range []string{"request_logs_bodies_hot", "request_logs_hot"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("scenario_driver 活代码不得再数 %s（停写后 delta 恒 0 = 断言永久 FAIL）", forbidden)
		}
	}
}
