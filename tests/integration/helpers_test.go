// 本文件为双模式存储架构端到端集成测试（dual_mode_test.go）与性能基准
// （benchmark_test.go）提供共享辅助构造函数。
//
// 包归属说明：本包为 tests/integration（package integration）。同目录下多数
// 历史文件带 //go:build integration 构建标签（依赖真实 PostgreSQL 等外部服务），
// 而双模式存储测试全部基于 t.TempDir() 与进程内实现，无任何外部服务依赖，
// 因此不带构建标签，使验收命令
//
//	go test -race -count=1 ./tests/integration/
//	go test -bench=. ./tests/integration/
//
// 无需附加 -tags 即可运行；带 -tags integration 运行时同样参与编译，行为一致。
package integration

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
	"github.com/kaixuan/llm-gateway-go/storage"
)

// dualTenant 集成测试统一使用的租户 ID。
const dualTenant = "tenant-dual"

// newLiteStorageConfig 基于给定根目录构造一份可用的 lite 模式存储配置。
// asyncWriters <= 0 时保持零值（工厂回落默认 4 个写 worker），
// > 0 时显式设置，供模式切换一致性子测试构造"不同配置"。
// 参数为 testing.TB，测试（*testing.T）与基准（*testing.B）均可复用。
func newLiteStorageConfig(t testing.TB, dir string, asyncWriters int) *storage.StorageConfig {
	t.Helper()
	return &storage.StorageConfig{
		Mode:           storage.StorageModeLite,
		SQLitePath:     dir + "/gateway.db",
		BodiesDir:      dir + "/bodies",
		CacheDir:       dir + "/cache",
		LogsDir:        dir + "/logs",
		AsyncWriters:   asyncWriters,
		MaxConnections: 2,
		Timeout:        5 * time.Second,
	}
}

// newDualFileCache 在 baseDir 下创建 L1.5 文件缓存（TTL 1 小时，上限 64MB），
// 供缓存链路测试与基准共用同一构造口径。
func newDualFileCache(t testing.TB, baseDir string) *v2.FileCache {
	t.Helper()
	fc, err := v2.NewFileCache(baseDir, time.Hour, 64<<20)
	require.NoError(t, err)
	return fc
}

// dualBody 构造一条含中文与结构化 RawMessage 的会话内容，turnNo 决定其唯一性。
// 中文负载用于验证 gzip 文件存储对多字节内容的往返无损。
func dualBody(sessionID string, turnNo int) *storage.SessionBody {
	return &storage.SessionBody{
		TenantID:  dualTenant,
		SessionID: sessionID,
		TurnNo:    turnNo,
		Timestamp: time.Now(),
		Request: []byte(fmt.Sprintf(
			`{"content":"第 %d 轮中文请求：请帮我总结这段对话要点","lang":"zh-CN","turn":%d}`,
			turnNo, turnNo)),
		Response: []byte(fmt.Sprintf(
			`{"content":"第 %d 轮中文响应：好的，以下是本次对话的总结。","tokens":%d}`,
			turnNo, 100+turnNo)),
		Metadata: map[string]interface{}{
			"model":    "glm-5",
			"scene":    "integration-test",
			"语言":       "中文",
			"turn_str": fmt.Sprintf("t%d", turnNo),
		},
	}
}

// dualState 构造一份带压缩元数据的 V2 会话状态，供缓存链路测试使用。
func dualState(sessionID string, lastTurnNo int) *v2.SessionStateV2 {
	return &v2.SessionStateV2{
		SessionID:  sessionID,
		TenantID:   dualTenant,
		LastTurnNo: lastTurnNo,
		UpdatedAt:  time.Now(),
		CompressionMeta: v2.CompressionMeta{
			Strategy:      "truncate_middle",
			TokenEstimate: 4096,
			MsgCount:      lastTurnNo * 2,
			SummaryMarker: "总结标记：本轮之前已完成压缩",
		},
	}
}
