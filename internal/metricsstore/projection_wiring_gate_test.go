package metricsstore

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 投影器接线门（2026-11-28 审计轮补判据）。
//
// ProjectionProcessor 是 /readyz/request-metrics 的唯一事实源——L142 轮之前
// NewProjectionProcessor 零调用点（「写了判定体没有调用点」的 team_budget_gate
// 同型缺陷）⇒ readiness 恒 503 ⇒ 部署水印门（DD_REQUIRE_REQUEST_METRICS=1）
// 全部超时回退/静默死亡。接线在 cmd/gateway/main.go 唯一装配点完成。
//
// 本门钉两件事：
//  1. main.go 必须真实构造 NewProjectionProcessor（装配点缺失 ⇒ readiness 失去事实源）；
//  2. 旧 Processor（(occurred_at, request_id) 复合游标，211 上自 2026-11-25 冻结）
//     不得回退重新装配——回退即恢复冻结形态。
func TestProjectionProcessorWiredInGatewayMain(t *testing.T) {
	src, err := os.ReadFile("../../cmd/gateway/main.go")
	require.NoError(t, err, "read gateway main source")
	body := string(src)

	assert.True(t, strings.Contains(body, "metricsstore.NewProjectionProcessor("),
		"cmd/gateway/main.go must construct metricsstore.NewProjectionProcessor "+
			"(readiness facts source unwired ⇒ deployment watermark gate always fails)")

	assert.False(t, strings.Contains(body, "metricsstore.NewProcessor("),
		"cmd/gateway/main.go must NOT wire the legacy frozen-cursor Processor; "+
			"wire NewProjectionProcessor instead (L142 补段)")
}
