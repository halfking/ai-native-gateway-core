package bg

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// ----------------------------------------------------------------------------
// 2026-11-07 探测时间点规划轮 —— 恢复及时性 × 节约性（F1/F2/F3/F4/F5）判据。
// 方案档：docs/audit/2026-11-07-凭据探测时间点规划与恢复及时性轮.md
// ----------------------------------------------------------------------------

// F2 判据：只有「天然只承载恢复复检」的 source 才豁免模型范围门。
// 变异臂：把豁免集合放大到 request_failure ⇒ 本表当场红；把泵改成不再
// 要求错误证据（pumpDueStatesSQL 丢掉 nodeProbeErrorEvidenceSQL）⇒ 下面的
// 泵-豁免联动判据当场红 —— 两条一起钉住「豁免不重开常态巡检（INV-1）」。
func TestAutomaticProbeSourceBypassesModelScope(t *testing.T) {
	cases := []struct {
		source string
		want   bool
	}{
		{"periodic", true},            // 泵：pumpDueStatesSQL 只挑错误证据行
		{"credential_recovery", true}, // 恢复事件验证探针：目标本就不可用
		{"request_failure", false},    // 真实流量触发：模型必在用量窗内，须过门
		{"selfcheck", false},          // daily audit / today-success：选点自带近窗流量
		{"admin", false},              // 手动（本就 Automatic=false，双保险）
		{"integrity_probe_planner", false},
		{"external_async", false},
		{"", false},
		{"periodic ", false}, // 精确匹配，不允许尾随空白混过
		{"Periodic", false},
	}
	for _, tc := range cases {
		if got := automaticProbeSourceBypassesModelScope(tc.source); got != tc.want {
			t.Errorf("automaticProbeSourceBypassesModelScope(%q) = %v, want %v", tc.source, got, tc.want)
		}
	}
}

// F2 豁免安全性联动判据：periodic 豁免之所以不破坏 INV-1，依赖
// pumpDueStatesSQL 的两道前置 —— 错误证据谓词 + paused=FALSE。这道门把
// 「豁免集与泵的选行条件」钉在同一处：任何一边放松都会当场红。
func TestPumpExemptionIsBackedByErrorEvidenceFilter(t *testing.T) {
	sql := pumpDueStatesSQL()
	if !strings.Contains(sql, nodeProbeErrorEvidenceSQL("nps")) {
		t.Fatal("pumpDueStatesSQL must filter rows by error evidence — the periodic scope exemption is only safe because every pumped row carries failure evidence")
	}
	if !strings.Contains(sql, "nps.paused = FALSE") {
		t.Fatal("pumpDueStatesSQL must exclude paused rows")
	}
}

// F3 判据：scope skip 的排程推迟必须 (a) 只动排程不动计数/错误字段，
// (b) 有「到期行才推」的上界 —— 不得把 30 天健康停放或中梯行拉进探测。
func TestDeferScopeSkippedRowSQLShape(t *testing.T) {
	sql := deferScopeSkippedRowSQL()
	for _, must := range []string{
		"paused = FALSE",
		"next_retry_at <= now() + $3::interval",
		"next_retry_at = now() + $3::interval",
	} {
		if !strings.Contains(sql, must) {
			t.Errorf("deferScopeSkippedRowSQL missing %q", must)
		}
	}
	for _, banned := range []string{
		"consecutive_failures", "last_err_code",
	} {
		if strings.Contains(sql, banned) {
			t.Errorf("deferScopeSkippedRowSQL must not touch execution-layer field %q", banned)
		}
	}
	if !strings.Contains(sql, "paused = FALSE") {
		t.Error("must respect operator pause")
	}
	if nodeProbeScopeSkipRecheck != 15*time.Minute {
		t.Errorf("scope skip recheck = %v, want 15m (45s would keep the pre-fix churn)", nodeProbeScopeSkipRecheck)
	}
}

// F3 行为腿：skip 分支把到期行推到重评点。用 pgxmock 断言 UPDATE 以正确的
// horizon 参数执行、失败时静默吞掉（业务 skip 语义是 (false, nil)）。
func TestDeferScopeSkippedRowAdvancesDueRow(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	w := &NodeProbeWorker{stateExecDB: mock}
	mock.ExpectExec(regexp.QuoteMeta(strings.TrimSpace(deferScopeSkippedRowSQL()))).
		WithArgs(7, "glm-5.3", "900 seconds").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	w.deferScopeSkippedRow(context.Background(), 7, "glm-5.3")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("defer UPDATE not issued as expected: %v", err)
	}
	// 失败必须吞掉（业务 skip 语义是 (false, nil)，不得升级成错误）
	mock.ExpectExec(regexp.QuoteMeta(strings.TrimSpace(deferScopeSkippedRowSQL()))).
		WillReturnError(errors.New("db down"))
	w.deferScopeSkippedRow(context.Background(), 7, "glm-5.3") // 不 panic 即可
}

// F4 纯判据：钉点 = recover_at + 60s；零值/已过期（含 60s 余量后）不可钉。
func TestQuotaResetPinDuration(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if d, ok := quotaResetPinDuration(time.Time{}, now); ok || d != 0 {
		t.Errorf("zero recover_at must not pin, got %v %v", d, ok)
	}
	if _, ok := quotaResetPinDuration(now.Add(-time.Hour), now); ok {
		t.Error("past recover_at must not pin")
	}
	if _, ok := quotaResetPinDuration(now.Add(-90*time.Second), now); ok {
		t.Error("recover_at older than the pin grace must not pin")
	}
	d, ok := quotaResetPinDuration(now.Add(7*24*time.Hour), now)
	if !ok {
		t.Fatal("future recover_at must pin")
	}
	want := 7*24*time.Hour + quotaResetPinAfter
	if d != want {
		t.Errorf("pin = %v, want %v (recover_at + %v)", d, want, quotaResetPinAfter)
	}
	// 边界在即（recover_at 在 60s 内）⇒ 目标点已到/将到 ⇒ 交给普通梯子
	if _, ok := quotaResetPinDuration(now.Add(30*time.Second), now); ok {
		t.Error("boundary-imminent recover_at should fall through to the ordinary ladder")
	}
}

// F4 门控判据：非配额状态/非配额体不钉；配额体 + 未来 recover_at 钉；
// DB 错误退回普通梯子（钉是优化，不是义务）。
func TestQuotaResetAlignedBackoffGates(t *testing.T) {
	quotaBody := `{"error":{"code":"1310","message":"您已达到每周使用上限，将于2026-11-14 01:00重置"}}`
	round := nodeProbeRoundResult{errCode: "http_429", httpStatus: 429, responseBody: quotaBody}

	t.Run("nil db falls through", func(t *testing.T) {
		if _, ok := quotaResetAlignedBackoff(context.Background(), nil, 7, round); ok {
			t.Fatal("nil db must fall through to the ordinary ladder")
		}
	})
	t.Run("non-quota status falls through", func(t *testing.T) {
		r := round
		r.httpStatus = 500
		r.errCode = "http_500"
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		if _, ok := quotaResetAlignedBackoff(context.Background(), mock, 7, r); ok {
			t.Fatal("http_500 must not consult the pin")
		}
	})
	t.Run("non-quota body falls through", func(t *testing.T) {
		r := round
		r.responseBody = `{"error":{"message":"too many requests, slow down"}}`
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		if _, ok := quotaResetAlignedBackoff(context.Background(), mock, 7, r); ok {
			t.Fatal("plain rate-limit body must not pin")
		}
	})
	t.Run("quota body + future recover_at pins", func(t *testing.T) {
		reset := time.Now().Add(7 * 24 * time.Hour)
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT quota_recover_at FROM credentials WHERE id = $1")).
			WithArgs(7).
			WillReturnRows(pgxmock.NewRows([]string{"quota_recover_at"}).AddRow(&reset))
		d, ok := quotaResetAlignedBackoff(context.Background(), mock, 7, round)
		if !ok {
			t.Fatal("weekly quota reset must pin")
		}
		if d <= 6*24*time.Hour || d > 7*24*time.Hour+2*time.Minute {
			t.Errorf("pin = %v, want ~recover_at+60s", d)
		}
	})
	t.Run("nil recover_at falls through", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT quota_recover_at FROM credentials WHERE id = $1")).
			WithArgs(7).
			WillReturnRows(pgxmock.NewRows([]string{"quota_recover_at"}).AddRow(nil))
		if _, ok := quotaResetAlignedBackoff(context.Background(), mock, 7, round); ok {
			t.Fatal("no recover_at ⇒ no pin (balance-type exhaustion keeps its periodic ladder)")
		}
	})
	t.Run("db error falls through", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT quota_recover_at FROM credentials WHERE id = $1")).
			WillReturnError(errors.New("db down"))
		if _, ok := quotaResetAlignedBackoff(context.Background(), mock, 7, round); ok {
			t.Fatal("db error must fall through to the ordinary ladder")
		}
	})
}

// F5 判据：事件回拉只动「未来排程 + 未成功 + 未暂停」的行 —— 到期行本来
// 就在被泵接管，健康停放行（last_direct_ok=TRUE）与操作员暂停行不得唤醒。
func TestRearmCredentialFailuresSQLShape(t *testing.T) {
	sql := rearmCredentialFailuresSQL()
	for _, must := range []string{
		"paused = FALSE",
		"next_retry_at > now()",
		"last_direct_ok IS DISTINCT FROM TRUE",
		"SET next_retry_at = now()",
	} {
		if !strings.Contains(sql, must) {
			t.Errorf("rearmCredentialFailuresSQL missing %q", must)
		}
	}
	if strings.Contains(sql, "consecutive_failures") || strings.Contains(sql, "last_err_code") {
		t.Error("rearm must not touch execution-layer fields")
	}
}

func TestRearmCredentialFailuresGuards(t *testing.T) {
	w := &NodeProbeWorker{}
	w.RearmCredentialFailures(0)  // credID<=0 no-op
	w.RearmCredentialFailures(-1) // no-op
	w2 := &NodeProbeWorker{}
	w2.RearmCredentialFailures(3) // nil exec no-op
	// pgx 路径：正常执行 + 失败吞掉
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	w3 := &NodeProbeWorker{stateExecDB: mock}
	mock.ExpectExec(regexp.QuoteMeta(strings.TrimSpace(rearmCredentialFailuresSQL()))).
		WithArgs(7).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	w3.RearmCredentialFailures(7)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("rearm UPDATE not issued: %v", err)
	}
	mock.ExpectExec(regexp.QuoteMeta(strings.TrimSpace(rearmCredentialFailuresSQL()))).
		WillReturnError(errors.New("db down"))
	w3.RearmCredentialFailures(7) // 不 panic
}

// F1 判据：恢复验证探针选模 —— default_probe_model 优先；空值落到
// available 优先的字典序第一绑定；两者皆无 ⇒ ""（调用方丢弃并告警）。
func TestResolveRecoveryProbeModel(t *testing.T) {
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
		m, err := ResolveRecoveryProbeModel(ctx, mock, 7)
		if err != nil || m != "glm-5.3" {
			t.Fatalf("m=%q err=%v", m, err)
		}
	})
	t.Run("falls back to first binding preferring available", func(t *testing.T) {
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
		m, err := ResolveRecoveryProbeModel(ctx, mock, 7)
		if err != nil || m != "minimax-m3.1-flash" {
			t.Fatalf("m=%q err=%v", m, err)
		}
	})
	t.Run("no model at all returns empty", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(default_probe_model, '') FROM credentials WHERE id = $1")).
			WithArgs(7).
			WillReturnRows(pgxmock.NewRows([]string{"m"}).AddRow("  "))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT pm.raw_model_name")).
			WithArgs(7).
			WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}))
		m, err := ResolveRecoveryProbeModel(ctx, mock, 7)
		if err != nil || m != "" {
			t.Fatalf("empty must be (\"\", nil), got m=%q err=%v", m, err)
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
		if _, err := ResolveRecoveryProbeModel(ctx, mock, 7); err == nil {
			t.Fatal("db error must surface so the wiring can log the drop")
		}
	})
	t.Run("guards", func(t *testing.T) {
		if m, err := ResolveRecoveryProbeModel(ctx, nil, 7); m != "" || err != nil {
			t.Errorf("nil db must be (\"\", nil), got %q %v", m, err)
		}
		if m, err := ResolveRecoveryProbeModel(ctx, nil, 0); m != "" || err != nil {
			t.Errorf("credID 0 must be (\"\", nil), got %q %v", m, err)
		}
	})
}

// F1 选模 SQL 的排序契约：available 优先、字典序稳定（与 v2 fallbackProbeModel 同形）。
func TestResolveRecoveryProbeModelPrefersAvailableBindings(t *testing.T) {
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
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).AddRow("a-model"))
	m, err := ResolveRecoveryProbeModel(context.Background(), mock, 7)
	if err != nil || m != "a-model" {
		t.Fatalf("m=%q err=%v", m, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected query shape: %v", err)
	}
}
