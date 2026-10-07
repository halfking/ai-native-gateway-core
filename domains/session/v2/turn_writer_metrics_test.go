package v2

// turn_writer_metrics_test.go — R77：sessions_v2_* 指标接线门。
//
// 缺陷背景：metrics/sessions_v2_metrics.go 声明了 6 个 sessions_v2_* 指标，
// 但**每一个标识符在全仓只有 1 处引用——就是它自己的声明**，从未被记录。
// 于是 domains/session/v2/README.md 的「监控指标」一节承诺的
// 「启用后可通过 Prometheus 监控」名不副实：/metrics 上这些 series 恒为
// ABSENT（不是 0），面板与告警永远拿不到数据点。
//
// 本门用 pgxmock 驱动真实的 appendTurnInLockedTx，断言计数器**真的动了**。
// 为什么必须是行为门而不是静态字符串断言：R74 的 RecordOutcome 教训——把埋点
// 放在函数末尾时，错误分支在 emit 之前就 return，静态门「看到了调用」却是绿的，
// 而指标依然零数据点。只有把函数真正跑起来才问得出「计数器动没动」。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/metrics"
)

// counterValue 读一个 prometheus.Collector 当前的值。
func counterValue(t *testing.T, c prometheus.Collector) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 4)
	c.Collect(ch)
	close(ch)
	var total float64
	n := 0
	for m := range ch {
		var pb dto.Metric
		require.NoError(t, m.Write(&pb))
		if pb.Counter != nil {
			total += pb.Counter.GetValue()
		} else if pb.Histogram != nil {
			total += float64(pb.Histogram.GetSampleCount())
		}
		n++
	}
	require.NotZero(t, n, "collector produced no metric — the series is ABSENT, not zero")
	return total
}

func delta(before, after float64) float64 { return after - before }

func TestTurnWriter_WritesSessionsV2Metrics(t *testing.T) {
	rec := TurnRecord{
		SessionID: "sess-metric", TenantID: "tenant-metric", RequestID: "req-metric",
		Ts: time.Now(), Model: "m", Provider: "p", Success: true,
	}

	t.Run("失败路径也计数（defer 覆盖错误分支）", func(t *testing.T) {
		okBefore := counterValue(t, metrics.SessionsV2WriteSuccess)
		failBefore := counterValue(t, metrics.SessionsV2WriteFailed)
		latBefore := counterValue(t, metrics.SessionsV2WriteLatency)

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		// 第一条语句就是 request 级 advisory lock；让它失败即可在最小 stub
		// 面上触发 early return——而 defer 仍然必须记账。
		mock.ExpectExec("session_turns_advisory_lock_key").
			WithArgs(rec.TenantID, "request:"+rec.RequestID).
			WillReturnError(errors.New("boom"))

		w := newTurnWriter(mock)
		_, err = w.appendTurnInLockedTx(context.Background(), mock, rec)
		// 关键：必须断言错误**来自我们 stub 的失败**，而不是任意错误。
		// 第一版只写 require.Error，pgxmock 的 "expected 0, but got 2 arguments"
		// 同样满足它 —— 门于是因为错误的原因而绿（实测抓到并修正）。
		require.ErrorContains(t, err, "boom",
			"the error must come from the stubbed advisory-lock failure, not from an "+
				"unmet pgxmock expectation")

		require.Equal(t, float64(0), delta(okBefore, counterValue(t, metrics.SessionsV2WriteSuccess)),
			"a failed turn write must not increment success")
		require.Equal(t, float64(1), delta(failBefore, counterValue(t, metrics.SessionsV2WriteFailed)),
			"a failed turn write must increment the failure counter — this is the branch a "+
				"metric placed at the end of the function would silently miss")
		require.Equal(t, float64(1), delta(latBefore, counterValue(t, metrics.SessionsV2WriteLatency)),
			"every attempt must contribute a latency observation, failures included")
	})

	t.Run("成功路径计数", func(t *testing.T) {
		okBefore := counterValue(t, metrics.SessionsV2WriteSuccess)
		failBefore := counterValue(t, metrics.SessionsV2WriteFailed)

		anyN := func(n int) []interface{} {
			a := make([]interface{}, n)
			for i := range a {
				a[i] = pgxmock.AnyArg()
			}
			return a
		}
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		mock.ExpectExec("session_turns_advisory_lock_key").
			WithArgs(rec.TenantID, "request:"+rec.RequestID).
			WillReturnResult(pgconn.NewCommandTag("SELECT 1"))
		// Hot arm first, partitioned parent only when hot answers "empty".
		// AddRow(1) is "first turn of a new session" (COALESCE(MAX,0)+1 with
		// zero rows), which is exactly the fallback case — so this stub must
		// now expect BOTH arms, and the returned turn_no stays 1.
		mock.ExpectQuery("MAX\\(turn_no\\).*FROM public\\.session_turns_hot").
			WithArgs(rec.TenantID, rec.SessionID).
			WillReturnRows(pgxmock.NewRows([]string{"next"}).AddRow(1))
		mock.ExpectQuery("MAX\\(turn_no\\)").
			WithArgs(rec.TenantID, rec.SessionID).
			WillReturnRows(pgxmock.NewRows([]string{"next"}).AddRow(1))
		mock.ExpectExec("INSERT INTO public\\.session_turns_hot").
			WithArgs(anyN(99)...). // $1..$99（$99 = §9.208 追加的 client_protocol，刻意排在末尾以免重排既有编号）
			WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
		mock.ExpectExec("t0_arrived_at").
			WithArgs(anyN(26)...). // 成功后的 enrich UPDATE
			WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))

		w := newTurnWriter(mock)
		turnNo, err := w.appendTurnInLockedTx(context.Background(), mock, rec)
		require.NoError(t, err, "the full success path must run; a stub gap here is a test defect")
		require.Equal(t, 1, turnNo)

		require.Equal(t, float64(1), delta(okBefore, counterValue(t, metrics.SessionsV2WriteSuccess)),
			"a successful turn write must increment the success counter")
		require.Equal(t, float64(0), delta(failBefore, counterValue(t, metrics.SessionsV2WriteFailed)),
			"a successful turn write must not increment the failure counter")
	})
}
