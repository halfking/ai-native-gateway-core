package admin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// F5-R2（2026-10-01 owner 拍板「删」）：admin 侧正文留存策略。
//
// 背景：keepAllBodies() 是死代码——全仓仅定义、无任何调用——而它的文档却声称
// 「bodies 只保留失败行，可省 ~90% 磁盘」。实测 admin ingest 对
// RequestBody/ResponseBody 无任何 success 过滤，即**全量落正文**。文档与实现
// 的偏差就是本次清理的根因。owner 裁决：删死代码、承认全量留存、把文档改到
// 与实现一致（已在 persistRequestLog 的 upsertRequestLogBodies 调用处补注）。
//
// 「成功行也落全量正文」这一行为**已由** telemetry_ingest_stop_write_gate_test.go
// 的 TestTelemetryIngestRequestLogStopWriteGate 钉住（Success:true + 门开 →
// 期望 request_logs_bodies_hot 的三段写入）。此处**不重复**该行为门，只补
// 它覆盖不到的一面：死代码本身不得复现。
//
// 为什么不钉「函数不存在」而钉 env 旋钮：死代码的危害不在于多一个函数，
// 而在于它带着「省 90% 磁盘」的**误导性文档**留在树里，运维照此做磁盘决策。
// 只断言 `func keepAllBodies(` 不存在，会被「改个名/加个别名/留个常量」绕过；
// 断言 env 旋钮不得复现才是对准失败特征的判据。

// TestKeepAllBodiesDeadCodeRemoved 钉住：误导性的 LLM_GATEWAY_KEEP_ALL_BODIES
// 旋钮不得复现。
//
// 判据打在**剥注释后的代码**上：先剥掉行/块注释再匹配，否则注释里为解释
// 历史而提到该名字就会被误判为「旋钮复现」。
func TestKeepAllBodiesDeadCodeRemoved(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(".", "telemetry.go"))
	require.NoError(t, err)
	code := stripGoComments(string(src))

	require.NotContains(t, code, "KEEP_ALL_BODIES",
		"LLM_GATEWAY_KEEP_ALL_BODIES 旋钮已按 F5-R2 裁决删除，不得复现：它声称「只留失败行、省 ~90% 磁盘」却是死代码，误导运维磁盘决策。admin 现状是全量落正文。")
	require.NotContains(t, code, "func keepAllBodies(",
		"keepAllBodies() 是已删除的死代码，不得复现。admin ingest 对成功/失败行一律落全量正文。")
}
