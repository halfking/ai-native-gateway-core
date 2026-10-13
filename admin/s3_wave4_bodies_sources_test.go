//go:build !integration

package admin

import (
	"strings"
	"testing"
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
// 的双源会话族化：turns 源×3（countSQL+主查询+LATERAL）、bodies 源×3，且
// v1 字面量、旧 ->'messages' 取键、ts 等值配对三个旧形态在**活代码**里
// 不再出现（注释里解释历史的文字不算，判据剥离注释——与 read inventory
// 同一处置）。
func TestS3Wave4CompressionSessionsTargetsSessionFamily(t *testing.T) {
	src := stripGoComments(s3Wave4ReadFile(t, "admin/compression_sessions.go"))
	if got := strings.Count(src, "SessionFamilyTurnsSourceSQL()"); got != 3 {
		t.Errorf("compression_sessions 的 turns 源必须出现 3 次（countSQL+主查询+LATERAL），实测 %d", got)
	}
	if got := strings.Count(src, "SessionFamilyBodiesSourceSQL()"); got != 3 {
		t.Errorf("compression_sessions 的 bodies 源必须出现 3 次（countSQL+主查询+LATERAL），实测 %d", got)
	}
	for _, forbidden := range []string{
		"request_logs_bodies_with_current_month",
		"request_logs_with_current_month",
		"->'messages'",
		"rb2.ts = rl2.ts",
		// ⚠ 形状守卫禁入：裸 jsonb_array_length 对 JSON 字面量 null（SQL 值
		// 非 NULL，COALESCE 不救）报 22023——2605 部署端点实测抓到的缺陷，
		// 修复=必须经 jsonb_typeof 判形。
		"jsonb_array_length(COALESCE(rb2.request_body",
	} {
		if strings.Contains(src, forbidden) {
			t.Errorf("compression_sessions 活代码不得再出现 %q（旧 v1 形态回退）", forbidden)
		}
	}
	if !strings.Contains(src, "jsonb_typeof(rb2.request_body)") {
		t.Error("compression_sessions 的数组长度必须先 jsonb_typeof 判形（JSON 字面量 null 是标量，裸 jsonb_array_length 报 22023）")
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
