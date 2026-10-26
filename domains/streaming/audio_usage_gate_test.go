package streaming

// audio_usage_gate_test.go —— 音频计量写点的**退役后**判据（2026-10-21 重写）。
//
// # 本文件为什么被整体重写（而不是补一条用例）
//
// 旧版钉的是 S4 停写门在音频写点上的两件事：
//
//	① 停写（storage.request_logs_write_enabled=false）时对 _request_logs_hot
//	   **零写入**；
//	② 门开时 INSERT 照常发出。
//
// ★ 迁移 890（v1 族退役）之后，**写点本身不存在了**：
// `domains/streaming/audio_usage_log.go` 的 `LogAudioUsage` 现在是
// **有文档的 no-op**（不再写任何库）。
//
// ⇒ 于是旧门的两条断言同时失效，方向还相反：
//
//	① 「停写时零写入」⇒ 现在**恒成立**（门开着也不写）⇒ 恒绿门
//	   （测了个寂寞，而且**看起来还是绿的**）；
//	② 「门开时 INSERT 发出」⇒ 现在**恒不成立** ⇒ 恒红门。
//
// ★★ 这就是「门红了先读门在守什么」的典型：不是实现坏了，
//   而是**门的前提（v1 族那张表）没了**。
//   ⇒ 处理办法不是把实现改回去（v1 族已被 890 退役），也不是把门删了
//     （那会连「退役后有没有留下可诊断的痕迹」一起丢掉），
//     而是**把门改钉退役后真正该保的东西**：
//
//	① no-op 语义：任何门状态下都**零 Exec**（不是「停写时零 Exec」）；
//	② 退役必须**留痕**：不许静默丢弃 —— 实现里要有日志调用
//	   （否则「音频秒数观测消失了」这件事**无人可诊断**）；
//	③ no-op 实现体里**不得再出现 INSERT**（防止有人把写点悄悄加回来，
//	   而门又因为「恒绿」看不见）。
//
// # 登记债（本门只登记，不负责还）
//
// 音频**秒数**（audio_seconds）观测在 v1 族退役后**没有会话族归宿**：
// `session_turns` / `session_turns_hot` 只有 audio_tokens，没有「秒数」；
// `usage_ledger` 侧的 audio_tokens 属**账务面**，与本写点不是一回事。
// ⇒ 与 `internal/trace.FlushToPG` 的 R52 语义一致（停写即丢弃）。
// ⇒ 这是**观测对象无归宿**的登记债（与 dashboard audio 维度同类），
//   不是 bug；要还它需要一次语义重设计（先定「秒数该落哪」），不在本轮范围。
//
// 变异验证：见 scripts/audit/verify-v1-retirement-red-gates.sh 的 M4/M5。

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// withAudioGateSettings 把 settings.Global 钉在给定停写值上，恢复交由
// t.Cleanup。形状复用 session_routing_test.go 的 fakeSettingsBackend。
func withAudioGateSettings(t *testing.T, writeEnabled bool) {
	t.Helper()
	prevGlobal := settings.Global
	t.Cleanup(func() { settings.Global = prevGlobal })

	store := map[string][]byte{
		"storage.request_logs_write_enabled": []byte(map[bool]string{true: "true", false: "false"}[writeEnabled]),
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, fakeSettingsBackend{store: store})
	for _, spec := range settings.StorageSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
}

// TestLogAudioUsageIsDocumentedNoOp 钉住「退役后的 no-op 语义」。
//
// ★ 关键点：**两种门状态都测**。
//
//	旧门只测「停写时零写入」⇒ 退役后恒绿（门开时它根本没测）。
//	现在把「门开」也测一遍，两条都必须零 Exec ⇒ 才叫「no-op」。
func TestLogAudioUsageIsDocumentedNoOp(t *testing.T) {
	for _, writeEnabled := range []bool{false, true} {
		name := "gate closed"
		if writeEnabled {
			name = "gate open"
		}
		t.Run(name+" still writes nothing", func(t *testing.T) {
			withAudioGateSettings(t, writeEnabled)
			pool, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer pool.Close()
			// 不注册任何期望：收到任何 Exec 都会让 mock.Stalled 报错。
			logger := NewPGAudioUsageLogger(pool)
			logger.LogAudioUsage(context.Background(), "default", "req-audio-nop", "minimax-m3", 12.5, 30)
			require.NoError(t, pool.ExpectationsWereMet())
		})
	}
}

// TestLogAudioUsageNoOpLeavesATrace 钉住「no-op 必须留痕」。
//
// ★ 为什么这条比「零写入」更重要：零写入有两种成因 ——
//
//	① 预期中的停写（可诊断：日志里有原因）；
//	② 写点被悄悄删了 / 表被退役了（**不可诊断**：没有任何痕迹）。
//	⇒ 观测面被丢弃而没人知道，是比「多写一行」更坏的事。
//	⇒ 这里要求实现里必须有日志调用。
func TestLogAudioUsageNoOpLeavesATrace(t *testing.T) {
	code := readAudioUsageSource(t)
	// ★ 判据必须钉**调用形态**，不能只钉「文件里出现过 slog.」。
	//   实测踩坑：只查 `slog.` 时，变异写成 `_ = slog.Default` 就能骗过它
	//   （那是**取值**不是**调用**）⇒ 门恒绿、等于没有门。
	//   ⇒ 这里要求出现**真正的日志调用**（`slog.X(`）。
	calls := []string{"slog.Debug(", "slog.Warn(", "slog.Info(", "slog.Error(", "slog.Log("}
	found := ""
	for _, c := range calls {
		if strings.Contains(code, c) {
			found = c
			break
		}
	}
	if found == "" {
		t.Fatalf("audio_usage_log.go 里没有任何**日志调用**（slog.Debug/Warn/Info/Error）—— " +
			"no-op 把「音频秒数观测被丢弃」这件事**静默**掉了 ⇒ 无痕的丢弃比有痕的丢弃更坏。" +
			"★ 退役后写点确实没了，但「它没了」必须留在日志里")
	}
}

// TestLogAudioUsageNoOpHasNoWriteStatement 钉住「no-op 实现体里不得再出现 INSERT」。
//
// ★ 这条是**防复发**的：旧门之所以失效，正是因为「门开着时 INSERT 照发」
//
//	与「v1 族退役后 INSERT 消失」这两件事**没有钉在一起** ——
//	退役改变了实现，而门还在钉退役前的行为。
//	⇒ 现在把「实现体里不出现 INSERT」单独钉住：有人把写点加回来时，
//	  这条会先红（而不是等到某个没人跑的门变红）。
func TestLogAudioUsageNoOpHasNoWriteStatement(t *testing.T) {
	code := readAudioUsageSource(t)
	// 只看**可执行文本**：剥掉行注释与块注释后再找 INSERT。
	stripped := stripGoLineComments(code)
	for _, needle := range []string{"INSERT INTO", "insert into", "_request_logs"} {
		if strings.Contains(stripped, needle) {
			t.Fatalf("audio_usage_log.go 的可执行文本里出现了 %q —— v1 族已被迁移 890 退役，"+
				"这个写点**没有去处**（会话族没有 audio_seconds 列；usage_ledger 是账务面、不是观测面）。"+
				"★ 若确实要恢复写入，先做一次语义重设计并同步改这道门", needle)
		}
	}
}

// readAudioUsageSource 读 audio_usage_log.go 的源码。
//
// ★ 为什么不用仓内已有的 `readStreamingSource`：它在
//
//	`failandmark_status_realdb_test.go` 里，而那个文件带 integration
//	build tag ⇒ 默认构建里**不存在** ⇒ 借用它会在普通 `go test` 下编译失败。
func readAudioUsageSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("audio_usage_log.go")
	if err != nil {
		t.Fatalf("读 audio_usage_log.go 失败：%v", err)
	}
	return string(raw)
}

// stripGoLineComments 去掉 Go 源码里的整行 `//` 注释与 /* */ 块注释。
func stripGoLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	out := b.String()
	// 块注释
	for {
		i := strings.Index(out, "/*")
		if i < 0 {
			break
		}
		j := strings.Index(out[i:], "*/")
		if j < 0 {
			out = out[:i]
			break
		}
		out = out[:i] + out[i+j+2:]
	}
	return out
}
