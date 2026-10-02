package admin

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// loadSessionTimelineInTx 的迭代错误传播钉测。
//
// 为什么需要它：这条查询从两个调用点各自的逐列相同实现里抽出来后
// （admin/session_analytics_handler.go:485、admin/session_panorama_handler.go:170），
// 迭代错误路径再无任何覆盖。丢一个 rows.Err() 是纯静默缺陷：编译全绿、vet 全绿、
// 其余单测全绿，端点却会把**截断的时间线**当完整结果返回——
// 表现为统计数字对得上、但轮次少了一段，比整体报错更难定位。
//
// 判据打在产物上：断言返回的 err 文本来自 rows.CloseError 注入的哨兵，
// 而不是断言「函数里有 rows.Err()」这种源码文本（那种判据会被注释满足，
// 也分不清它是在 for 循环之后还是在里面提前 return）。
//
// 同时钉住空结果契约：无行时必须返回 nil 而不是空切片。两个调用点对空结果的
// JSON 形态历史不一致（analytics 序列化成 null，panorama 序列化成 []），
// 由调用方各自还原；在这一层"顺手统一"会悄悄改掉其中一条 API 的响应契约。

// timelineMockRow 造一行与 sessionTimelineQuery 的 15 个投影列对齐的数据。
// 顺序必须与查询里的 SELECT 列表严格一致，少一个会让 Scan 报列数不符。
func timelineMockRow() *pgxmock.Rows {
	ts := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	// 注意列类型必须与 RequestEvent 的字段类型逐个对齐：work_type /
	// compression_strategy 是 *string，cache_read_tokens 是 *int，
	// 而 prompt_tokens / completion_tokens / latency_ms 是值类型 int。
	// 类型写错时 pgxmock 报的是 "destination kind 'ptr' not supported"，
	// 看起来像生产代码的 Scan bug，实际是夹具不对。
	workType, compression, cacheRead := "chat", "none", 0
	preview, responsePreview := "hello", "world"
	return pgxmock.NewRows([]string{
		"request_id", "ts", "success", "client_model", "outbound_model",
		"prompt_tokens", "completion_tokens", "cost_usd", "latency_ms",
		"work_type", "compression_strategy", "cache_read_tokens",
		"error_kind", "request_preview", "response_preview",
	}).AddRow(
		"req-1", ts, true, "gpt-4o", "gpt-4o",
		100, 50, 0.01, 1200,
		&workType, &compression, &cacheRead,
		nil, &preview, &responsePreview,
	)
}

// TestLoadSessionTimelineInTx_RowsErrPropagates 是本文件的主门：
// 迭代中途失败必须以非 nil error 返回，且**不能**把已扫到的半截行当成成功结果
// 交给调用方（timeline 必须为 nil）。
func TestLoadSessionTimelineInTx_RowsErrPropagates(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	sentinel := errors.New("simulated timeline iteration failure")
	rows := timelineMockRow().CloseError(sentinel)
	mock.ExpectQuery(`SELECT[\s\S]*FROM request_logs_with_current_month[\s\S]*gw_session_id`).
		WithArgs("sess-rows-err").
		WillReturnRows(rows)

	timeline, err := loadSessionTimelineInTx(context.Background(), mock, "sess-rows-err", "")
	if err == nil {
		t.Fatalf("expected iteration error to propagate, got nil (timeline len=%d)", len(timeline))
	}
	if !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatalf("expected error to carry the rows.Err() sentinel %q, got %v", sentinel.Error(), err)
	}
	if len(timeline) != 1 {
		t.Fatalf("fixture should yield one row before the failure, got %d", len(timeline))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("mock expectations: %v", err)
	}
}

// TestLoadSessionTimelineInTx_CallersDiscardPartialTimelineOnError 是防止部分
// 时间线外泄到响应的真正那道门。
//
// 上面的主门已经证明错误会传播，但它同时**带着已扫到的半截行一起返回**
// （Go 惯例：返回值部分可用 + err）。所以防外泄的机制不在这个函数里，
// 而在于两个调用点是否在 err 非 nil 时立即放弃 timeline——
// 这正是原注释里点名的那类故障：「时间线空白/截断但统计数字对得上」。
//
// 为什么用纯文本守卫判调用点：调用点各自在事务回调里做包装
// （analytics 外面套 fmt.Errorf("timeline query: %w")，panorama 直接透传），
// 形态各异但契约相同——拿到 err 就 return。剥掉注释后匹配调用语句后的
// err 判断，避免注释里出现同样的代码文本把门喂绿。
func TestLoadSessionTimelineInTx_CallersDiscardPartialTimelineOnError(t *testing.T) {
	// 紧邻判据：把调用语句本身连同其后的第一个非空语句抠出来，用 ^\s*if err != nil
	// 锚定。**不能**退化成「调用点之后 N 字符内包含 if err != nil」——
	// 那样 300 字符窗口会吃到后面 buildSessionAnalysisInTx 的 err 判断，
	// 于是把 timeline 的 err 丢掉、让调用点继续跑，守卫仍然全绿。
	// 变异验证：把 `timeline, err :=` 改成 `timeline, _ :=` 后本门转红。
	callSiteRE := regexp.MustCompile(
		`(?s)([^\n]*loadSessionTimelineInTx\(ctx, tx, gwSessionID, tenantID\))\s*\n\s*([^\n]*)\n`)

	for _, tc := range []string{"session_analytics_handler.go", "session_panorama_handler.go"} {
		t.Run(tc, func(t *testing.T) {
			raw, err := os.ReadFile(tc)
			if err != nil {
				t.Fatalf("read %s: %v", tc, err)
			}
			code := stripGoComments(string(raw))
			m := callSiteRE.FindStringSubmatch(code)
			if m == nil {
				t.Fatalf("%s: no longer matches the expected call shape "+
					"(`<var>, err := loadSessionTimelineInTx(ctx, tx, ...)`) — 迁移已变，钉测需重写", tc)
			}
			// err 必须真的接住返回值：`_` 接收就等于丢弃错误。
			if !strings.Contains(m[1], "err") {
				t.Fatalf("%s: loadSessionTimelineInTx 的返回值必须接住 err，"+
					"当前捕获为 %q —— 丢弃 err 会让截断的时间线进入响应", tc, m[1])
			}
			next := strings.TrimSpace(m[2])
			if !strings.HasPrefix(next, "if err != nil") {
				t.Fatalf("%s: 调用之后的第一条语句必须是 `if err != nil`，"+
					"当前是 %q —— 丢弃 err 会让截断的时间线进入响应", tc, next)
			}
		})
	}
}

// TestLoadSessionTimelineInTx_QueryErrPropagates 覆盖查询本身失败。
// 与上一条分开是因为两者走的是不同的返回语句：Query 的 err 在拿 rows 之前，
// 迭代的 err 在 for 之后。合成一条会漏掉其中一半的改写。
func TestLoadSessionTimelineInTx_QueryErrPropagates(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT[\s\S]*FROM request_logs_with_current_month`).
		WithArgs("sess-query-err").
		WillReturnError(errors.New("simulated timeline query failure"))

	timeline, err := loadSessionTimelineInTx(context.Background(), mock, "sess-query-err", "")
	if err == nil {
		t.Fatalf("expected query error to propagate, got nil (timeline len=%d)", len(timeline))
	}
	if timeline != nil {
		t.Fatalf("timeline must be nil on query error, got %d rows", len(timeline))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("mock expectations: %v", err)
	}
}

// TestLoadSessionTimelineInTx_EmptyResultStaysNil 钉住注释里写明的那条契约：
// 无行时返回 nil（不是空切片）。判据用 == nil 而不是 len()==0 —— 后者
// 分不出 nil 和 []RequestEvent{}，而这两个值序列化出来的 JSON 形态不同
// （null vs []），正是 analytics 与 panorama 两条线的差异所在。
func TestLoadSessionTimelineInTx_EmptyResultStaysNil(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT[\s\S]*FROM request_logs_with_current_month`).
		WithArgs("sess-empty").
		WillReturnRows(pgxmock.NewRows([]string{
			"request_id", "ts", "success", "client_model", "outbound_model",
			"prompt_tokens", "completion_tokens", "cost_usd", "latency_ms",
			"work_type", "compression_strategy", "cache_read_tokens",
			"error_kind", "request_preview", "response_preview",
		}))

	timeline, err := loadSessionTimelineInTx(context.Background(), mock, "sess-empty", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if timeline != nil {
		t.Fatalf("empty result must stay nil so callers keep their own JSON shape, got %#v", timeline)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("mock expectations: %v", err)
	}
}

// TestLoadSessionTimelineInTx_TenantFilterBindsSecondArg 钉住租户分支。
// super admin 传空 tenantID 时不加谓词、带租户时谓词必须绑到 $2 ——
// 顺序反了会把会话 id 送进 tenant_id 谓词，表现为"查得到但恒为空"，
// 而这类错误在没有任何数据的时间窗里与"真的没有轮次"无法区分。
func TestLoadSessionTimelineInTx_TenantFilterBindsSecondArg(t *testing.T) {
	t.Run("with tenant", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock.NewPool: %v", err)
		}
		defer mock.Close()

		mock.ExpectQuery(`WHERE gw_session_id = \$1 AND tenant_id = \$2`).
			WithArgs("sess-tenant", "tenant-a").
			WillReturnRows(pgxmock.NewRows([]string{
				"request_id", "ts", "success", "client_model", "outbound_model",
				"prompt_tokens", "completion_tokens", "cost_usd", "latency_ms",
				"work_type", "compression_strategy", "cache_read_tokens",
				"error_kind", "request_preview", "response_preview",
			}))

		if _, err := loadSessionTimelineInTx(context.Background(), mock, "sess-tenant", "tenant-a"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("mock expectations: %v", err)
		}
	})

	t.Run("super admin omits tenant predicate", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock.NewPool: %v", err)
		}
		defer mock.Close()

		// 用 QueryMatcherFunc 抓实际 SQL 做否定断言：空 tenant 时绝不能出现
		// tenant_id 谓词。Contains 判 "tenant_id" 会假阳性 —— 投影里没有该列，
		// 但一旦有人加回投影就会误判，所以这里判谓词形态。
		var actual string
		pool, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(
			pgxmock.QueryMatcherFunc(func(_, real string) error {
				actual = real
				return nil
			})))
		if err != nil {
			t.Fatalf("pgxmock.NewPool: %v", err)
		}
		defer pool.Close()

		pool.ExpectQuery(`.*`).WithArgs("sess-super").WillReturnRows(pgxmock.NewRows([]string{
			"request_id", "ts", "success", "client_model", "outbound_model",
			"prompt_tokens", "completion_tokens", "cost_usd", "latency_ms",
			"work_type", "compression_strategy", "cache_read_tokens",
			"error_kind", "request_preview", "response_preview",
		}))

		if _, err := loadSessionTimelineInTx(context.Background(), pool, "sess-super", ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(actual, "tenant_id =") {
			t.Fatalf("super admin scope must not bind a tenant predicate, got SQL:\n%s", actual)
		}
		if !strings.Contains(actual, "gw_session_id = $1") {
			t.Fatalf("session predicate lost, got SQL:\n%s", actual)
		}
	})
}

// TestLoadSessionTimelineInTx_QueryCarriesNoFormatArtifacts 判生成结果里不得出现
// fmt 动词残渣。这条查询用 strconv.Itoa 拼 LIMIT 而不是 Sprintf，注释里也点明了
// 原因；守卫钉住这个选择，防止有人"顺手简化"回 Sprintf 格式串拼接。
// 判据打 %!( / %!(MISSING / %!(int= 这些产物的失败特征，而不是判"没有 Sprintf"——
// 后者在别处合法使用时会产生假阴性。
func TestLoadSessionTimelineInTx_QueryCarriesNoFormatArtifacts(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(
		pgxmock.QueryMatcherFunc(func(_, real string) error { return nil })))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	var actual string
	mock2, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(
		pgxmock.QueryMatcherFunc(func(_, real string) error {
			actual = real
			return nil
		})))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock2.Close()

	mock2.ExpectQuery(`.*`).WithArgs("sess-fmt").WillReturnRows(pgxmock.NewRows([]string{
		"request_id", "ts", "success", "client_model", "outbound_model",
		"prompt_tokens", "completion_tokens", "cost_usd", "latency_ms",
		"work_type", "compression_strategy", "cache_read_tokens",
		"error_kind", "request_preview", "response_preview",
	}))

	if _, err := loadSessionTimelineInTx(context.Background(), mock2, "sess-fmt", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, bad := range []string{"%!", "%!(MISSING", "%!(int=", "%!s("} {
		if strings.Contains(actual, bad) {
			t.Fatalf("generated SQL carries fmt artifact %q, got SQL:\n%s", bad, actual)
		}
	}
	// LIMIT 必须真的带上了 sessionTimelineLimit 的值。
	if !strings.Contains(actual, "LIMIT 100") {
		t.Fatalf("expected LIMIT %d to be appended, got SQL:\n%s", sessionTimelineLimit, actual)
	}
}
