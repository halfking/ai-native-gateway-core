package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// 本文件钉的是 mcp_gateway_uptime_seconds 这条指标的**值级**正确性。
//
// ★ 为什么不能只靠 internal/metricguard 那道门：它判的是「生产代码里有没有
//
//	记录调用」（令牌级「标识符.记录方法」形态）。接线了 ⇒ 门绿，
//	但**值是不是对的**它一个字都没说。而这条指标曾经栽的坑恰恰是：
//	面板与告警「看起来存在」、实际永远拿不到数据点。
//
// ★ 所以这里断言的是**值**：注入时钟 ⇒ 运行时长可确定性算出来 ⇒
//
//	断言「gauge 的值 == 算出来的运行时长」，而不是「有个数就算过」。
//
// ★ 而「值对不对」还取决于**刷新挂在哪儿**：Prometheus 的抓取是外部触发的，
//
//	刷新若不在抓取路径上，值就会停在某一次旧读数上 ⇒ 用**哨兵值**做对照。

// fakeClock 是可注入、可推进的单调时钟。
type fakeClock struct{ t time.Time }

// now 返回当前时刻（签名与 Config.Now 一致）。
func (c *fakeClock) now() time.Time { return c.t }

// advance 把时钟往前推。
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newClockedGateway 构造一个时钟可控的网关。
func newClockedGateway(t *testing.T, clock *fakeClock) *Gateway {
	t.Helper()
	g, _ := newTestGateway(t, nil, func(c *Config) { c.Now = clock.now })
	return g
}

// metricLineFrom 取出 exposition 文本里属于 name 的那一行（失败时给可读证据）。
func metricLineFrom(text, name string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, name+" ") {
			return line
		}
	}
	return ""
}

// TestUptimeSecondsUsesInjectedClock 断言运行时长走**网关自己的时钟**。
//
// ★ 这是整条链路的地基：若 UptimeSeconds 还在用 time.Since（真实时钟），
//
//	下面那条「gauge 值 == 90」的断言就没法确定性成立（真实时间会漂）。
func TestUptimeSecondsUsesInjectedClock(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	g := newClockedGateway(t, clock)
	clock.advance(90 * time.Second)

	if got := g.UptimeSeconds(); got != 90 {
		t.Fatalf("UptimeSeconds() = %d, want 90（注入时钟没被用上？）", got)
	}
}

// TestRefreshUptimeMetricRecordsRuntimeLength 断言刷新把**运行时长**写进 gauge。
func TestRefreshUptimeMetricRecordsRuntimeLength(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	g := newClockedGateway(t, clock)
	clock.advance(90 * time.Second)

	g.RefreshUptimeMetric()
	if got := testutil.ToFloat64(uptimeSeconds); got != 90 {
		t.Fatalf("uptimeSeconds = %v, want 90", got)
	}
}

// TestScrapeHandlerRefreshesUptimeMetricBeforeScrape 断言**抓取路径上**确实刷新。
//
// ★ 哨兵值法：先把 gauge 写成一个别的值（-1），再发一次抓取。
//   ⇒ 若抓取路径没接上，响应里就是哨兵 -1 而不是 42 ⇒ 这条断言**有牙**；
//   ⇒ 而若只断言「body 里有这个指标名」，一个残留的旧值就能骗过它。
func TestScrapeHandlerRefreshesUptimeMetricBeforeScrape(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	g := newClockedGateway(t, clock)
	clock.advance(42 * time.Second)

	// ★ 哨兵：gauge 是包级全局量，前一个用例已经写过 90 了；
	//   这里显式改写成 -1，才能区分「刷新了」与「读到的是上一次的值」。
	uptimeSeconds.Set(-1)

	srv := httptest.NewServer(g.ScrapeHandler(promhttp.Handler()))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(raw)

	line := metricLineFrom(text, "mcp_gateway_uptime_seconds")
	if line != "mcp_gateway_uptime_seconds 42" {
		t.Fatalf("/metrics 里的 uptime 行 = %q, want %q\n"+
			"  （若拿到的是哨兵 -1，说明抓取路径没有真正刷新）",
			line, "mcp_gateway_uptime_seconds 42")
	}
}

// TestControlPlaneStatsSnapshotCarriesRefreshedUptime 断言控制面统计出口
// 也在收集**前**刷新（否则快照里那份是上一次的读数）。
func TestControlPlaneStatsSnapshotCarriesRefreshedUptime(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	g := newClockedGateway(t, clock)
	clock.advance(77 * time.Second)

	// 哨兵同上：区分「刷新了」与「读到旧值」。
	uptimeSeconds.Set(-1)

	rec := httptest.NewRecorder()
	NewControlPlane(g).handleStats(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /stats = %d, want 200", rec.Code)
	}

	var body struct {
		Families []struct {
			Name    string `json:"name"`
			Metrics []struct {
				Value *float64 `json:"value"`
			} `json:"metrics"`
		} `json:"families"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	var found bool
	for _, fam := range body.Families {
		if fam.Name != "mcp_gateway_uptime_seconds" {
			continue
		}
		found = true
		if len(fam.Metrics) == 0 || fam.Metrics[0].Value == nil {
			t.Fatalf("uptime 族存在但没有样本：%+v", fam)
		}
		if *fam.Metrics[0].Value != 77 {
			t.Fatalf("uptime 样本值 = %v, want 77（快照没刷新？）", *fam.Metrics[0].Value)
		}
	}
	if !found {
		t.Fatalf("统计快照里没有 mcp_gateway_uptime_seconds 族：%s", rec.Body.String())
	}
}
