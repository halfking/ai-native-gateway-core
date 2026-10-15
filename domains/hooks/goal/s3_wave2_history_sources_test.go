//go:build !integration

package goal

import (
	"strings"
	"testing"
)

// TestS3Wave2HistorySources（2026-10-13 S3 波 2）钉住会话历史重建的双源
// 形状。停写后新会话正文只在 session_bodies；镜像前历史只在
// _request_logs_bodies。判据同层：断言驱动包级 SQL 变量——它们就是
// FetchBySession 实际发给 PostgreSQL 的文本。
func TestS3Wave2HistorySources(t *testing.T) {
	s := historySessionSQL
	if got := strings.Count(s, "session_id = $1"); got != 2 {
		t.Errorf("会话族腿应有内推的 session_id = $1 两处（hot+parent，实存 %d）", got)
	}
	// bodies 腿必须是 request_id 单键 JOIN：bodies 源不投影 ts，
	// ts 等值 JOIN 会解析期 42703（响亮失败），但静默换源是更坏方向。
	if !strings.Contains(s, "ON rb.request_id = rl.request_id") {
		t.Error("bodies 腿必须按 request_id 单键 JOIN（helper 合同）")
	}
	if strings.Contains(s, "rb.ts = rl.ts") {
		t.Error("bodies JOIN 不得带 ts 等值（两侧 ts 99.85% 不等，静默丢正文）")
	}
	for _, col := range []string{"rb.request_body", "rb.response_body"} {
		if !strings.Contains(s, col) {
			t.Errorf("投影缺 %q（v1 同名列合同）", col)
		}
	}
	v := historyV1SQL
	for _, frag := range []string{"FROM _request_logs rl", "_request_logs_bodies rb", "rl.gw_session_id = $1"} {
		if !strings.Contains(v, frag) {
			t.Errorf("v1 回退腿缺少 %q（镜像前历史会话的正文会断供）", frag)
		}
	}
}
