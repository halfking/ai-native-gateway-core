// data/probe_schedule_defaults_test.go
//
// D09 · data —— 探测调度配置的登记面/选模面契约（2026-11-07 探测时间点规划轮）。
//
// D-01 probe.recovery_max_backoff_seconds 的 spec 登记必须维持「恢复 SLA」语义
//      （默认 300，范围 60..21600）——F4 的钉点与 B-01 的封顶都以此为锚。
// D-02 恢复事件验证探针选模（ResolveRecoveryProbeModel）：
//      default_probe_model 优先 → available 优先的字典序第一绑定 → 无模可探返回空
//      （F1 修复：空模型此前被 Submit 静默丢弃）。
//
// 跑测：
//   go test -race -timeout 60s ./tests/48h-audit/D09-node-state-selfcheck/data/...

package data

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/pashagolub/pgxmock/v4"
)

// resolve bridges the unexported pickDB parameter: pgxmock 的池在结构上满足
// bg.pickDB（QueryRow/Query/Exec 同签名），可直接传入。
func resolve(ctx context.Context, db pgxmock.PgxPoolIface, credID int) (string, error) {
	return bg.ResolveRecoveryProbeModel(ctx, db, credID)
}

// TestData_RecoveryBackoffSpecRegistered 钉恢复 SLA 旋钮的登记面：默认 300s、
// 可调范围 60..21600（21600 = 纯配置回退到旧长尾 5m→1h→2h→6h 的档位）。
func TestData_RecoveryBackoffSpecRegistered(t *testing.T) {
	var found *settings.Spec
	for _, s := range settings.ProbeSpecs() {
		if s.Key == "probe.recovery_max_backoff_seconds" {
			found = s
			break
		}
	}
	if found == nil {
		t.Fatal("probe.recovery_max_backoff_seconds 未登记 —— 恢复 SLA 旋钮失联")
	}
	if found.Default != 300 {
		t.Errorf("Default = %v, want 300（5min 恢复 SLA）", found.Default)
	}
	if found.Min == nil || *found.Min != 60 {
		t.Errorf("Min = %v, want 60", found.Min)
	}
	if found.Max == nil || *found.Max != 21600 {
		t.Errorf("Max = %v, want 21600（纯配置回退档）", found.Max)
	}
}

// TestData_ResolveRecoveryProbeModel 钉 F1 选模链：default 优先 / 绑定回退 /
// 全无返回空（调用方据此丢弃并告警，而不是提交注定 endpoint_build 失败的探针）。
func TestData_ResolveRecoveryProbeModel(t *testing.T) {
	ctx := context.Background()

	t.Run("default_probe_model wins", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(default_probe_model, '') FROM credentials WHERE id = $1")).
			WithArgs(7).
			WillReturnRows(pgxmock.NewRows([]string{"m"}).AddRow("glm-5.3"))
		m, err := resolve(ctx, mock, 7)
		if err != nil || m != "glm-5.3" {
			t.Fatalf("m=%q err=%v", m, err)
		}
	})

	t.Run("falls back to binding", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(default_probe_model, '') FROM credentials WHERE id = $1")).
			WithArgs(7).
			WillReturnRows(pgxmock.NewRows([]string{"m"}).AddRow(""))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT pm.raw_model_name")).
			WithArgs(7).
			WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).AddRow("minimax-m3.1-flash"))
		m, err := resolve(ctx, mock, 7)
		if err != nil || m != "minimax-m3.1-flash" {
			t.Fatalf("m=%q err=%v", m, err)
		}
	})

	t.Run("no model at all", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(default_probe_model, '') FROM credentials WHERE id = $1")).
			WithArgs(7).
			WillReturnRows(pgxmock.NewRows([]string{"m"}).AddRow(""))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT pm.raw_model_name")).
			WithArgs(7).
			WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}))
		m, err := resolve(ctx, mock, 7)
		if err != nil || m != "" {
			t.Fatalf("want (\"\", nil), got (%q, %v)", m, err)
		}
	})

	t.Run("db error surfaces", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(default_probe_model, '') FROM credentials WHERE id = $1")).
			WillReturnError(errors.New("db down"))
		if _, err := resolve(ctx, mock, 7); err == nil {
			t.Fatal("DB 错误必须上浮（调用方要打点丢弃原因）")
		}
	})
}
