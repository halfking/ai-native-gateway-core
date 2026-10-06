package bg

// 「观察源健康必须在基准价 catalog 为空时也留痕」这条判据。
//
// # 缺口（2026-10-06 真机读数撞出来的）
//
// 真机 `public.model_baseline_price_observation_health` 存在但**0 行**，而
// `bg/data/model_baseline_prices.json` 的 `models` 也是 `[]`。把两件事对着
// `RunBaselineReconciliation` 的控制流一看就明白了 —— 修复前它是：
//
//	catalog, err := LoadEmbeddedBaselineCatalog()
//	if len(catalog) == 0 { slog.Warn(...); return }   ← 早退
//	observed, url, at, err := FetchMachineReadablePrices(ctx, client)
//	if err != nil { recordObservationFailure(...); return }
//	recordObservationSuccess(ctx, db, url, len(observed))
//
// ⇒ SSOT 为空时每轮都在第一行之后返回：**一次源都不抓、一次健康行都不写**。
// 而 `baseline_observation_stale` 的 SQL 是
// `SELECT ... FROM model_baseline_price_observation_health WHERE consecutive_failures > 0
//  OR last_success_at IS NULL OR last_success_at < now() - interval '24 hours'`
// —— 它只读**已经存在的行**。表 0 行 ⇒ 返回 0 行 ⇒ 健康面报「无问题」。
//
// ★ 这正是迁移 832 存在的理由被从背面复现。那份迁移头写得很清楚：反复失败
//   且不留痕「比压根没有对账更坏 —— 后者让人知道自己在裸奔，前者让人以为
//   自己在看仪表盘」。而早退路径制造的是同一个状态，且**连一次失败都没发生
//   过**，比反复失败更难发现。
//
// # 为什么既有判据抓不到
//
// `bg/observation_health_test.go` 里有两条，都不经过 `RunBaselineReconciliation`：
//
//   · `TestReconcileWorkerRecordsEveryFetchOutcome` 读**源码文本**，只保证
//     `recordObservationFailure(ctx, db, url, err)` 那一行存在于文件里。它防的
//     是「有人把那行当冗余删掉」，不是「有人把早退放回它上面」。
//   · `TestObservationHealthRecordsBothOutcomesAndResetsOnSuccess` 直接调
//     recordObservationSuccess / recordObservationFailure，验的是**那两个函数
//     写得对**。把整个 `run` 换成空实现，它照样全绿。
//
// ⇒ 缺的正是「控制流顺序」这一段。本判据跑真的 `RunBaselineReconciliation`。
//
// # 承重
//
//	A 两臂都要留痕：成功源与失败源各一次。**只测成功臂不够** —— 把
//	  `recordObservationFailure` 那行删掉，成功臂照样绿。
//	B 目的地侧断言：直接 SELECT 832 那张表，不量 worker 的返回值。
//	C 不依赖真网络：`http.Client` 换成一个只回固定 JSON 的 RoundTripper。
//	  这不是 mock 掉被测逻辑 —— 解析、URL、状态码判定、计数全都走真代码，
//	  只有传输层被替掉；真去打 models.dev 会让判据在断网时变成一次偶发红。
//	D 卸炸弹：worker 是 `run()` + 12h ticker 的死循环，所以先起 goroutine、
//	  轮询等那行落库、再 cancel。**不能预先 cancel** —— ctx 已经取消时
//	  `http.NewRequestWithContext` 会立刻失败，于是「成功臂」会被测成失败臂，
//	  而失败臂照样绿 ⇒ 两条判据一起失去意义。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具表已存在时也跳过（它要建表）。

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// roundTripperFunc 把一个函数变成 http.RoundTripper。
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// cannedObservationJSON 是 models.dev 形状的最小可解析载荷：一个 provider、
// 一个模型、in/out 两个价。FetchMachineReadablePrices 的解析器要求它非空，
// 否则会走「observation source yielded no priced models」那条错误路径
// （而 observed_models=0 本来就该算失败，语义不同，见 observed_models 列注释）。
const cannedObservationJSON = `{
  "acme": {
    "name": "Acme",
    "doc": "https://acme.example/pricing",
    "models": {
      "acme-large": {
        "id": "acme-large",
        "cost": {"input": 3.0, "output": 15.0}
      }
    }
  }
}`

func jsonResponder(body string, status int) roundTripperFunc {
	return func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	}
}

// runOneReconcileCycle 起一个 worker、轮询到健康表出现目标行、然后收工。
// 返回那一行的内容；等不到就返回 ok=false 而不是直接失败——判定留给调用方，
// 这样「等不到」和「写错了」在报错里是两种不同的信息。
func runOneReconcileCycle(t *testing.T, pool *pgxpool.Pool, rt roundTripperFunc, sourceURL string) (lastSuccessNull bool, failures int, observed int, ok bool) {
	t.Helper()
	// 固定 URL，免得真去打外网（observationSourceURL 认这个环境变量）。
	t.Setenv(machineReadablePricingURLEnv, sourceURL)
	// kill switch 默认是开的（opt-out），这里显式写出来是为了让读代码的人
	// 看到这条判据不依赖「环境里恰好没设它」。
	t.Setenv(baselineSyncEnvKillSwitch, "1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	// ★ 显式传**空** catalog，而不是去读内嵌 SSOT。
	//
	// 这条判据要测的是「catalog 为空时的控制流」。原先它靠
	// `if len(catalog) != 0 { t.Fatalf("SSOT 必须真空") }` 来保证这一点 ——
	// 也就是把被测不变量绑在一个**正要填满的全局状态**上。SSOT 一旦有了
	// 第一条基准价（填 SSOT 正是这个 worker 存在的目的），这条判据就永久
	// 失效，且失效方式是 t.Fatalf：一条永远红的门，理由写着「SSOT 必须真空」，
	// 会误导下一个人去删基准价来「修好」它。
	//
	// 2026-10-06：RunBaselineReconciliation 已按 RunChecks/runChecks 的同款
	// 理由抽出 runBaselineReconcileOnce(ctx, db, client, catalog)，catalog
	// 成为参数 ⇒ 判据不再依赖 SSOT 的内容。
	go func() {
		defer close(done)
		runBaselineReconcileOnce(ctx, pool, &http.Client{Transport: rt}, nil)
	}()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx, `
			SELECT last_success_at IS NULL, consecutive_failures, COALESCE(observed_models, -1)
			  FROM public.model_baseline_price_observation_health
			 WHERE source_url = $1`, sourceURL).
			Scan(&lastSuccessNull, &failures, &observed); err == nil {
			cancel()
			<-done
			return lastSuccessNull, failures, observed, true
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	return false, 0, 0, false
}

func newObservationHealthPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name='model_baseline_price_observation_health'`).
		Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skip("the fixture table public.model_baseline_price_observation_health already exists " +
			"— this test drops it")
	}
	if _, err := pool.Exec(ctx, observationHealthFixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DROP TABLE IF EXISTS public.model_baseline_price_observation_health`)
	})
	return pool
}

// TestReconcileRecordsSourceHealthEvenWhenBaselineCatalogIsEmpty 是主判据。
//
// 前置自证：内嵌 SSOT 仍必须**可加载**（每条过 validate）。本判据**不再**要求
// 它为空 —— 空 catalog 是显式传进去的参数，与 SSOT 的内容解耦（2026-10-06）。
//
// 为什么必须解耦：原先的 `if len(catalog) != 0 { t.Fatalf }` 把被测不变量绑在
// 一个正要填满的全局状态上。SSOT 有了第一条基准价（填 SSOT 正是这个 worker
// 存在的目的）之后它会永久红，而红的原因写着「SSOT 必须真空」——那会把下
// 一个人引去删基准价。
func TestReconcileRecordsSourceHealthEvenWhenBaselineCatalogIsEmpty(t *testing.T) {
	// 量具自证：内嵌 SSOT 本身**必须仍是可加载的**（每条过 validate）。
	// 判据不再要求它为空 —— 空 catalog 现在是**显式传进去的参数**。
	// 顺带把条目数打进日志，让「这条判据在 SSOT 有 N 条的状态下跑过」可核对。
	if catalog, err := LoadEmbeddedBaselineCatalog(); err != nil {
		t.Fatalf("load the embedded catalog: %v", err)
	} else {
		t.Logf("embedded SSOT currently carries %d model(s); this criterion injects an empty "+
			"catalog on purpose, so it is independent of that number", len(catalog))
	}

	// ---- 臂 1：源可读 ⇒ 必须记成功 ----
	pool := newObservationHealthPool(t)
	const okURL = "https://observation-ok.invalid/api.json"
	nullSuccess, failures, observed, ok := runOneReconcileCycle(t, pool,
		jsonResponder(cannedObservationJSON, http.StatusOK), okURL)
	if !ok {
		t.Fatal("no health row appeared for a healthy source with an empty baseline catalog — " +
			"the worker's empty-catalog early return skips the fetch AND the recording, so " +
			"baseline_observation_stale reads an empty table and reports nothing")
	}
	if nullSuccess {
		t.Error("last_success_at is NULL after a successful fetch")
	}
	if failures != 0 {
		t.Errorf("consecutive_failures=%d, want 0 after a successful fetch", failures)
	}
	if observed != 1 {
		t.Errorf("observed_models=%d, want 1 — the canned payload has exactly one priced model. "+
			"A wrong count here would mean the parser silently dropped it (observed_models=0 is "+
			"defined as a FAILURE state, so miscounting it inverts the signal)", observed)
	}

	// ---- 臂 2：源不可读 ⇒ 必须记失败（否则「只测成功臂」的空转就成立） ----
	// ★ 必须换一个 source_url。两个理由，缺一不可：
	//   1) 否则轮询会立刻命中臂 1 留下的行并 cancel，worker 被
	//      `context canceled` 掐死，来不及写这一轮（实测 rc 0 但行没变）。
	//   2) recordObservationFailure 的 upsert **不清** last_success_at
	//      （只 +1 consecutive_failures），所以「先成功后失败」时它仍非 NULL。
	//      「首轮就失败 ⇒ last_success_at IS NULL」是 INSERT 路径的性质，换 URL
	//      才测得到。
	const failURL = "https://observation-fail.invalid/api.json"
	nullSuccess, failures, _, ok = runOneReconcileCycle(t, pool,
		jsonResponder("", http.StatusInternalServerError), failURL)
	if !ok {
		t.Fatal("no health row appeared for a failing source with an empty baseline catalog — " +
			"recordObservationFailure is unreachable on the empty-catalog path")
	}
	if !nullSuccess {
		t.Error("last_success_at is NOT NULL after a failing fetch — a failed source must not " +
			"keep a success timestamp, or the staleness comparison never fires")
	}
	if failures != 1 {
		t.Errorf("consecutive_failures=%d, want 1 after a single failing fetch", failures)
	}

	// ---- 最后一环：健康面真的响了 ----
	//
	// 到目前为止断言的都是「表里有一行」。但这条判据要回答的是
	// 「运维看仪表盘时能不能看见」，而那取决于 baseline_observation_stale
	// 的**真查询**会不会返回这一行。中间隔着一层「写对了 ⇒ 检查读得到」的
	// 假设时，只验前者会漏掉一类真实故障：upsert 的列名与检查 SQL 的列名
	// 对不上（两边都合法，单独看都绿）。
	def := healthCheckByID(t, "baseline_observation_stale")
	if !def.Optional {
		t.Error("baseline_observation_stale lost its Optional flag — a missing table would then " +
			"abort the whole health round instead of skipping one check")
	}
	var flagged int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM (`+def.Query+`) q`).Scan(&flagged); err != nil {
		t.Fatalf("run the health check query: %v", err)
	}
	if flagged != 1 {
		t.Errorf("baseline_observation_stale reported %d row(s), want exactly 1 — the failing "+
			"source must reach the health surface, otherwise the whole point of 832 is lost. "+
			"(Note: its SQL only reads rows that already exist, which is why the empty-catalog "+
			"early return was invisible before this fix.)", flagged)
	}
}
