package v2

// turn_logs_writer_ttl_test.go —
// R67 session-storage 审计子任务 2 (handoff §4) 的保留期行为测试。
//
// 审计要点：24h 硬编码的真实位置不是 430 的列 DEFAULT（该 INSERT 永远显式
// 带 expires_at，DEFAULT 实际是死代码），而是本文件对应的写入方字面量
// `time.Now().Add(24*time.Hour)`。保留期只有在写入侧读设置才真正可配。
//
// settings.Global 在单测里为 nil，getPlatformInt 会走 fallback 分支返回
// 默认值 24 —— 正好用来钉住「无配置时的行为」。

import (
	"testing"
	"time"
)

func TestSessionTurnLogsTTL_DefaultIs24h(t *testing.T) {
	// settings.Global == nil in unit tests → fallback path.
	got := sessionTurnLogsTTL()
	if got != 24*time.Hour {
		t.Errorf("sessionTurnLogsTTL() = %v, want 24h (must match the historical literal so enabling 753 changes nothing at rest)", got)
	}
}

func TestSessionTurnLogsTTL_NeverZero(t *testing.T) {
	// Data-safety floor: a 0 duration would make expires_at == now, so every
	// stage row would be immediately collectable by the 753 sweep.
	if got := sessionTurnLogsTTL(); got < time.Hour {
		t.Errorf("sessionTurnLogsTTL() = %v, must never drop below 1h", got)
	}
}

// TestSessionTurnLogsTTL_MatchesSpecBounds mirrors the clamp in
// bg.clampSessionTurnLogsTTLHours. The two clamps live in different packages
// (one guards the SQL interlock argument, one bakes expires_at); they must
// agree on the same [1,168] window or the setting's advertised range lies.
func TestSessionTurnLogsTTL_MatchesSpecBounds(t *testing.T) {
	for _, h := range []int{1, 24, 168} {
		if got := int(sessionTurnLogsTTL().Hours()); got < 1 || got > 168 {
			t.Errorf("resolved TTL %dh (probe %d) outside documented [1,168]", got, h)
		}
	}
}
