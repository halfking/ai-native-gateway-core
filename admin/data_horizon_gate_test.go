// data_horizon_gate_test.go — 「流量数据地平线」告示门（2026-10-15 r1033 重写）。
//
// # 这道门在防什么
//
// 新判据回答「网关有流量时，session_turns 是否在记录」。两种最危险的失效：
//
//	① 空闲误报 —— 网关没有流量（或刚重启），session_turns 没有新行是**正常**
//	   状态；把空闲判成断流，横幅会变成背景噪音，真正断流那天没人当回事。
//	② 断流静默 —— 有流量而 session_turns 没跟上时不告示，页面照常展示
//	   滞后数字，所有门都绿（「没有报错」正是这一档的定义）。
//
// ⇒ 逐格覆盖「流量 × 新鲜度」矩阵的每一格；两头的失效形态各有专测。
//
// 为什么判定器是纯函数：两个真信号（DB 的 MAX(ts)、hub 的 lastActivity）
// 在单测里没有注入口，门若去拉真 DB/真 hub 会变成集成测试。逻辑全部在
// dataHorizonNoticeFor(三参数)，接线层由形状判据钉住。
package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

// readAdminSourceFile 按文件名读 admin 包源码（形状钉子用）。
func readAdminSourceFile(name string) (string, error) {
	for _, dir := range []string{".", ".."} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			return string(b), nil
		}
	}
	return "", os.ErrNotExist
}

var (
	dhNow        = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	dhFreshSeen  = dhNow.Add(-2 * time.Minute)  // 2 分钟前有请求（窗内）
	dhStaleSeen  = dhNow.Add(-8 * time.Minute)  // 8 分钟前有请求（仍在 10 分钟窗内）
	dhAgedSeen   = dhNow.Add(-25 * time.Minute) // 25 分钟前（滑出窗）
	dhFreshData  = dhNow.Add(-1 * time.Minute)  // session 数据 1 分钟前
	dhLaggingData = dhNow.Add(-20 * time.Minute) // session 数据落后最近请求 18 分钟
)

// TestHorizonHealthyWhenTrafficAndSessionFresh —— 钉住失效形态 ② 的反面。
func TestHorizonHealthyWhenTrafficAndSessionFresh(t *testing.T) {
	notice, ok := dataHorizonNoticeFor(dhFreshData, dhFreshSeen, dhNow)
	if ok || notice != nil {
		t.Errorf("流量新鲜 + session 新鲜时返回了告示（ok=%v, notice=%+v）。\n"+
			"  一个「永远显示」的横幅在真正断流那天的信息量是零。", ok, notice)
	}
}

// TestHorizonStaleWhenTrafficOutrunsSession —— 钉住失效形态 ②。
func TestHorizonStaleWhenTrafficOutrunsSession(t *testing.T) {
	notice, ok := dataHorizonNoticeFor(dhLaggingData, dhStaleSeen, dhNow)
	if !ok || notice == nil {
		t.Fatalf("网关 8 分钟前仍有请求、session 数据落后 18 分钟，却没有告示。\n"+
			"  这正是断流的定义形态：接口照常返回，没有任何人能从 UI 上看出。")
	}
	if !notice.Stale || notice.Unknown {
		t.Errorf("断流格的标记错误：Stale=%v Unknown=%v（期望 true/false）。", notice.Stale, notice.Unknown)
	}
	for _, needle := range []string{"session_turns"} {
		if !strings.Contains(notice.Source, needle) {
			t.Errorf("告示 Source 未指向 session_turns：%q", notice.Source)
		}
	}
}

// TestHorizonStaleWhenSessionEmptyButTrafficRecent —— 热表一行都没有 + 网关
// 刚接过请求 = 断流本身（零时间恒早于 lastSeen-lag）。
func TestHorizonStaleWhenSessionEmptyButTrafficRecent(t *testing.T) {
	notice, ok := dataHorizonNoticeFor(time.Time{}, dhFreshSeen, dhNow)
	if !ok || notice == nil || !notice.Stale {
		t.Fatalf("session 空 + 流量在窗内 ⇒ 应判 stale（ok=%v notice=%+v）。", ok, notice)
	}
}

// TestHorizonIdleNeverAlarms —— 钉住失效形态 ①（本轮最关键的新判别力）。
//
// 两个空闲格：无流量信号（hub 零值）；信号已滑出近期窗。两格下 session
// 即便为空/极旧也不得告示——没有流量就没有「该到而未到的数据」。
func TestHorizonIdleNeverAlarms(t *testing.T) {
	cases := []struct {
		name string
		seen time.Time
		data time.Time
	}{
		{"hub 零值（启动以来无请求）+ session 空", time.Time{}, time.Time{}},
		{"hub 零值 + session 极旧", time.Time{}, dhNow.Add(-48 * time.Hour)},
		{"流量滑出窗 + session 空", dhAgedSeen, time.Time{}},
		{"流量滑出窗 + session 极旧", dhAgedSeen, dhNow.Add(-48 * time.Hour)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			notice, ok := dataHorizonNoticeFor(c.data, c.seen, dhNow)
			if ok || notice != nil {
				t.Errorf("空闲格返回了告示（ok=%v, notice=%+v）。\n"+
					"  空闲时没有新数据是正常状态；告示它 = 教会用户忽略横幅。", ok, notice)
			}
		})
	}
}

// TestHorizonToleranceBoundary —— 容忍窗边界的两格：踩线=健康，越线=断流。
func TestHorizonToleranceBoundary(t *testing.T) {
	// lastSeen - 5min 恰好等于 sessionMax：Before 为假 ⇒ 健康。
	if _, ok := dataHorizonNoticeFor(dhFreshSeen.Add(-dataHorizonLagTolerance), dhFreshSeen, dhNow); ok {
		t.Error("session 数据恰在容忍线上（= lastSeen-5m）被判 stale。")
	}
	// 再早 1 毫秒就越线。
	if notice, ok := dataHorizonNoticeFor(dhFreshSeen.Add(-dataHorizonLagTolerance-time.Millisecond), dhFreshSeen, dhNow); !ok || !notice.Stale {
		t.Error("session 数据越容忍线 1ms 却没判 stale。")
	}
}

// TestHorizonUnknownNeverClaimsFreshness —— unknown 态的语义与措辞红线：
// 不说 stale（没证据），也绝不断言数据是新的。
func TestHorizonUnknownNeverClaimsFreshness(t *testing.T) {
	notice, ok := dataHorizonNoticeForUnknown()
	if !ok || notice == nil {
		t.Fatal("unknown 构造器没有产出告示。")
	}
	if !notice.Unknown || notice.Stale {
		t.Errorf("Unknown=%v Stale=%v（期望 true/false）：没有测到断流却断言断流，是编造事实。", notice.Unknown, notice.Stale)
	}
	for _, banned := range []string{"数据是新的", "仍在记录", "未停更"} {
		if strings.Contains(notice.Effect+notice.Silence, banned) {
			t.Errorf("unknown 态文案出现肯定式断言 %q——「读不到」时不得替数据背书。", banned)
		}
	}
}

// TestHorizonNilDBReturnsUnknown —— 端点在无 DB（lite/未接库）时必须走
// unknown，而不是 panic 或谎报健康。
func TestHorizonNilDBReturnsUnknown(t *testing.T) {
	h := &Handler{}
	notice, ok := h.dataHorizonNotice(nil)
	if !ok || notice == nil || !notice.Unknown {
		t.Fatalf("nil DB 应返回 unknown（ok=%v notice=%+v）。", ok, notice)
	}
}

// TestDataHorizonEndpointShape —— 端点载荷契约：
//   · 键 `data_horizon` 恒在；值为 null **或** 带 stale/unknown 的对象；
//   · 绝不接受 stale=false && unknown=false 的对象（那应表现为 null）；
//   · stale 与 unknown 互斥。
func TestDataHorizonEndpointShape(t *testing.T) {
	h := &Handler{} // nil DB ⇒ unknown 分支，正好钉住对象形态
	rec := httptest.NewRecorder()
	h.handleDataHorizon(rec, httptest.NewRequest("GET", "/api/admin/data-horizon", nil))
	if rec.Code != 200 {
		t.Fatalf("状态码 = %d，期望 200（这个端点不能报错：它报的就是「有没有静默失效」）。", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（体：%s）", err, rec.Body.String())
	}
	v, present := body["data_horizon"]
	if !present {
		t.Errorf("响应缺 `data_horizon` 键（体：%s）。前端按「该键为 null ⇒ 健康」判；键不存在会让它拿到 undefined。", rec.Body.String())
	}
	if v != nil {
		obj, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("data_horizon 既不是 null 也不是对象：%#v", v)
		}
		stale, _ := obj["stale"].(bool)
		unknown, _ := obj["unknown"].(bool)
		if stale && unknown {
			t.Errorf("stale=true 同时 unknown=true：%#v——两个互斥态同时断言。", obj)
		}
		if !stale && !unknown {
			t.Errorf("出现了 stale=false && unknown=false 的对象：%#v——健康必须表现为 null。", obj)
		}
	}
	if _, ok := body["//"]; !ok {
		t.Error("响应缺 `//` 解释键。抓包的人需要知道 null 是什么意思。")
	}
	// 头契约：unknown 态打 unknown 值。
	if got := rec.Header().Get(DataHorizonHeader); got != "unknown" {
		t.Errorf("unknown 态响应头 = %q，期望 %q", got, "unknown")
	}
}

// TestHubLastRequestSeenStartsAtZero —— 钉住 hub lastActivity 的零值初始化。
//
// 若有人改回 time.Now() 初始化，空闲网关在每次重启后的近期窗内会被
// dataHorizonNoticeFor 误判 stale（重启时刻冒充「最后一次请求」）。
// 这道门让那个回归当场红。
func TestHubLastRequestSeenStartsAtZero(t *testing.T) {
	hub := NewLiveStreamSSEHub(nil, LiveStreamConfig{})
	if seen := hub.LastRequestSeenAt(); !seen.IsZero() {
		t.Errorf("新建 hub 的 LastRequestSeenAt = %v，期望零值——\n"+
			"  创建时刻冒充流量信号会让空闲网关重启后被误判断流。", seen)
	}
}

// TestHubTrafficSignalCountsBusinessOnly —— 信号只认业务请求的形状钉子。
//
// 探针/auto 请求不进会话族（S4 实测：auto 请求不产生 session_turns 行），
// 若广播循环不按 IsProbe 过滤，「只有探针在跑」的窗口会被误报断流
// （lastActivity 新鲜 + session 数据「落后」）。Run 循环在单测里不可驱动，
// 这里钉住源形状：broadcast 分支必须以 !req.IsProbe 守卫业务时间戳。
func TestHubTrafficSignalCountsBusinessOnly(t *testing.T) {
	src, err := readAdminSourceFile("live_stream_sse.go")
	if err != nil {
		t.Fatalf("读 live_stream_sse.go 失败：%v", err)
	}
	start := strings.Index(src, "case req := <-h.broadcast:")
	if start < 0 {
		t.Fatal("找不到 broadcast 分支——hub 主循环结构变了，本钉子需要人工复核。")
	}
	segment := src[start : start+600]
	if !strings.Contains(segment, "lastBusinessActivity = time.Now()") {
		t.Errorf("broadcast 分支没有推进业务时间戳；got:\n%s", segment)
	}
	guard := strings.Index(segment, "lastBusinessActivity = time.Now()")
	probeGuard := strings.LastIndex(segment[:guard], "IsProbe")
	if probeGuard < 0 {
		t.Errorf("业务时间戳的推进没有被 IsProbe 守卫（探针流量会误报断流）；got:\n%s", segment)
	}
}

// TestSessionTurnsHotMaxTSHandlesNULL —— 空表的 NULL 扫描钉子。
//
// 本地 2646 首测抓到的实现 bug：MAX(ts) 在空表上返回 SQL NULL，直接扫进
// time.Time 报 "cannot scan NULL" → 空库被误报成 unknown。修法是扫
// sql.NullTime 并归一为零值（零值由纯函数按「有流量但无记录 = 断流」
// 裁决，而不是当成「读不到」）。
func TestSessionTurnsHotMaxTSHandlesNULL(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	t.Run("空表 NULL ⇒ 零值且无错", func(t *testing.T) {
		mock.ExpectQuery(`SELECT MAX\(ts\) FROM public\.session_turns_hot`).
			WillReturnRows(pgxmock.NewRows([]string{"max"}).AddRow(nil))
		ts, err := sessionTurnsHotMaxTSFrom(context.Background(), mock)
		if err != nil {
			t.Fatalf("空表 MAX(NULL) 不应报错（那会把空库误报成 unknown）：%v", err)
		}
		if !ts.IsZero() {
			t.Errorf("空表 MAX 应归一为零值，got %v", ts)
		}
	})

	t.Run("有值 ⇒ 原样返回", func(t *testing.T) {
		want := time.Date(2026, 10, 15, 5, 51, 53, 0, time.UTC)
		mock.ExpectQuery(`SELECT MAX\(ts\) FROM public\.session_turns_hot`).
			WillReturnRows(pgxmock.NewRows([]string{"max"}).AddRow(want))
		ts, err := sessionTurnsHotMaxTSFrom(context.Background(), mock)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ts.Equal(want) {
			t.Errorf("got %v, want %v", ts, want)
		}
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations: %v", err)
	}
}
//
// 板上所有 fallback 查询（summary/trends/pies/drill/overview counts）走
// boardRequestLogsFromClause；本门钉住它 FROM 会话族两腿（hot ∪ parent），
// 且不再是纯 v1 的 without_customer_id 包装视图。
// TestBoardFallbackSourceIsSessionFamily —— 看板 fallback 换源的形状钉子。
//
// 板上所有 fallback 查询（summary/trends/pies/drill/overview counts）走
// boardRequestLogsFromClause；本门钉住它 FROM 会话族两腿（hot ∪ parent），
// 且不再是纯 v1 的 without_customer_id 包装视图。
func TestBoardFallbackSourceIsSessionFamily(t *testing.T) {
	from, alias := boardRequestLogsFromClause()
	if alias != "r" {
		t.Fatalf("alias = %q，期望 r（板上全部 fallback SQL 按 r 拼接）。", alias)
	}
	for _, needle := range []string{"session_turns_hot", "session_turn_details_hot", "session_turns ", "request_status"} {
		if !strings.Contains(from, needle) {
			t.Errorf("board fallback FROM 缺 %q；got:\n%s", needle, from)
		}
	}
	if strings.Contains(from, "without_customer_id") {
		t.Errorf("board fallback 仍在用纯 v1 包装视图 without_customer_id（S4 后冻结）：%s", from)
	}
}

// TestUsageShortWindowSourceIsSessionHot —— requestLogsFromClause 两臂的
// 形状钉子：≤7d 走会话族热腿（原物理 _request_logs_hot 已冻结），
// >7d 保持 canonical 视图（session 分支 + v1 历史存量）。
func TestUsageShortWindowSourceIsSessionHot(t *testing.T) {
	from, alias := requestLogsFromClause(3)
	if alias != "r" {
		t.Fatalf("short-window alias = %q，期望 r。", alias)
	}
	if !strings.Contains(from, "session_turns_hot") {
		t.Errorf("≤7d 臂未走会话族热腿：%s", from)
	}
	if strings.Contains(from, "_request_logs_hot") {
		t.Errorf("≤7d 臂仍在读冻结的物理热表：%s", from)
	}
	fromLong, _ := requestLogsFromClause(30)
	if !strings.Contains(fromLong, "request_logs_with_current_month") {
		t.Errorf(">7d 臂应保持 canonical 视图：%s", fromLong)
	}
}
