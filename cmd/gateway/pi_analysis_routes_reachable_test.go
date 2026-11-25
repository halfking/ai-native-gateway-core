package main

// PI 智能体管理 API 的**路由可达性 + 鉴权**守门测试（2026-11-17 审计轮）。
//
// ★ 为什么需要这道门（本仓有前车之鉴，AGENTS.md §708①）：
//
//	handler 层单测「自己 new 一个 handler 直接打」⇒ **覆盖不到**
//	「路由到底注册到 main mux 没有」这件事：
//	  · handler 写好、RegisterRoutes 写好，但**没被 wire 到 main mux**
//	    ⇒ 前端全 404，而 handler 单测**仍全绿**；
//	  · 更隐蔽的一种：某条**更长的前缀路由**（如 `/api/admin/`）
//	    把静态路由**吞掉** ⇒ 同一个 404，单测同样全绿。
//
//	⇒ 所以这里必须**真发 HTTP 请求**，并断言响应**不是 404**。
//
// ★★ 本文件同时钉住 **P0：端点必须鉴权**（2026-11-17 审计轮新发现）：
//
//	PI 错误分析端点（/api/admin/pi-analysis/*）曾被**无鉴权**注册：
//	  · main.go 用 `mux.Handle("/api/admin/", e)` 把带 admin JWT 中间件的
//	    **Echo 实例**挂在该前缀上；
//	  · 但 Go 的 ServeMux 按**最长模式**派发 ⇒
//	    `/api/admin/pi-analysis/queue` 这条**精确路由更长**
//	    ⇒ 请求**不进 Echo** ⇒ **绕过 JWT 中间件**；
//	  · ⇒ 任何人都能读走错误队列（内部错误消息 + 租户/会话/请求 id），
//	    并 **POST /trigger** 触发 LLM 分析（免费的算力放大器 + DoS）。
//
//	⇒ 修法：RegisterRoutes 用 AdminMiddleware 包住全部端点；
//	  且**鉴权材料缺失 ⇒ 拒绝注册**（fail-closed）——
//	  宁可端点 404，也不能让一个「忘了配 secret」的部署把内部队列敞在公网上。
//
// ★ 断言 404 之外的状态码是刻意的：
//
//	管理端点全部走 admin.AdminMiddleware ⇒ 无凭据时返回 **401**。
//	「401」证明「路由**在** mux 里、且中间件**跑到了**」；
//	「404」证明「路由没注册 / 被吞掉 / 鉴权材料缺失而被拒绝注册」。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin"
)

// piAnalysisRoutePaths 是 PI 智能体管理 API 的**完整端点清单**。
//
// ★ 清单必须与 admin/pi_analysis_handler.go 的 RegisterRoutes 一一对应；
//
//	少写一个 ⇒ 那个端点就失去了可达性保护（门会变绿，但端点已经 404）。
var piAnalysisRoutePaths = []struct {
	method string
	path   string
}{
	{http.MethodGet, "/api/admin/pi-analysis/queue"},
	{http.MethodGet, "/api/admin/pi-analysis/results"},
	{http.MethodGet, "/api/admin/pi-analysis/results/1"},
	// ★ 2026-11-18 PI 分析管理面优化轮：queue 单条详情 + apply/revert。
	{http.MethodGet, "/api/admin/pi-analysis/queue/1"},
	{http.MethodPost, "/api/admin/pi-analysis/results/1/apply"},
	{http.MethodPost, "/api/admin/pi-analysis/results/1/revert"},
	{http.MethodPost, "/api/admin/pi-analysis/trigger"},
	{http.MethodGet, "/api/admin/pi-analysis/stats"},
	// ★ 2026-11-25 取证轮：按需取证 / 定向分析 / 复测 / 重解析（单条与批量）。
	{http.MethodGet, "/api/admin/pi-analysis/queue/1/evidence"},
	{http.MethodPost, "/api/admin/pi-analysis/queue/1/analyze"},
	{http.MethodPost, "/api/admin/pi-analysis/queue/1/verify"},
	{http.MethodPost, "/api/admin/pi-analysis/results/1/reparse"},
	{http.MethodPost, "/api/admin/pi-analysis/results/reparse-failed"},
}

// newTestPIGatewayPool 造一个**惰性**连接池（不会真的连库）。
//
// ★ 为什么需要它：admin.AdminMiddleware 的签名要 `*pgxpool.Pool`，
//
//	而无凭据请求在 AdminMiddleware 里**直接 401 返回**、根本不碰池子
//	⇒ 池子只需要「非 nil 且配置合法」即可。
func newTestPIGatewayPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(),
		"postgres://pi_test:pi_test@127.0.0.1:1/pi_test?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("create lazy pgx pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestPIAnalysisRoutesReachableOverHTTP 真发 HTTP 请求断言端点**可达且被鉴权**。
//
// ★ 为什么要造**竞争路由**：
//
//	真实 main mux 上有 `/api/admin/` 这类**前缀**路由（Echo 实例）。
//	Go 的 ServeMux 按「最长模式优先」⇒ 静态路由不会被前缀路由吞掉，
//	但这条性质必须**被证明**，不能靠「Go 的规则就是这样」的空口断言
//	⇒ 这里在同一个 mux 上真的注册一条前缀路由，再打真请求。
func TestPIAnalysisRoutesReachableOverHTTP(t *testing.T) {
	mux := http.NewServeMux()

	// ★ 竞争路由（与真实 main mux 同型）：`/api/admin/` 前缀 ——
	//   main.go 用它挂 Echo（带 admin JWT 中间件）。
	//   注册**顺序刻意放在 PI 路由之前**（Go 的 ServeMux 与注册顺序无关，
	//   只看模式长度 ⇒ 这正是要证明的性质）。
	mux.HandleFunc("/api/admin/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("admin prefix catch-all"))
	})
	mux.HandleFunc("/api/admin/pi-analysis/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("pi prefix catch-all"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("root catch-all"))
	})

	// ★ 被测对象：admin 包的 RegisterRoutes（带鉴权材料）。
	h := admin.NewPIAnalysisHandler(nil, nil, nil, newTestPIGatewayPool(t), "test-secret")
	h.RegisterRoutes(mux, "/api/admin/pi-analysis")

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	for _, tc := range piAnalysisRoutePaths {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode == http.StatusNotFound {
				t.Fatalf("★ 端点 %s %s 返回 404 —— 路由没有注册，或被某条更长的"+
					"前缀路由吞掉了。\n"+
					"★ 这正是本仓 §708① 的事故形态：handler 单测全绿，前端却全 404。",
					tc.method, tc.path)
			}
			// 期望 401（无凭据 ⇒ AdminMiddleware 拒绝）。
			// 若是 418，说明被前缀 catch-all 吞掉了 —— 同样是不可达。
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("端点 %s %s：期望 401（无凭据被 AdminMiddleware 拒绝），"+
					"实际 %d —— 路由可能被竞争路由吞掉，或**中间件没有生效**"+
					"（后者意味着端点无鉴权）",
					tc.method, tc.path, resp.StatusCode)
			}
		})
	}
}

// TestPIAnalysisRoutesFailClosedWithoutAuthMaterial 钉住 P0 的**兜底一半**：
// 鉴权材料缺失 ⇒ **拒绝注册**（端点落到调用方自己的处理器），而不是「裸奔」。
//
// ★ 为什么必须 fail-closed：
//
//	如果「secret 没配 ⇒ 不加鉴权照样注册」，那部署者少配一个环境变量
//	就会把内部错误队列敞在公网上 —— 而且**没有任何日志或报错**提示这件事。
//	⇒ 宁可是 404（运维会发现页面打不开），也不能是 200（数据在漏）。
func TestPIAnalysisRoutesFailClosedWithoutAuthMaterial(t *testing.T) {
	cases := []struct {
		name   string
		pool   *pgxpool.Pool
		secret string
	}{
		{"no pool no secret", nil, ""},
		{"pool but no secret", newTestPIGatewayPool(t), ""},
		{"secret but no pool", nil, "test-secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			// 调用方自己的处理器：端点**若被注册**就会命中它（418）。
			mux.HandleFunc("/api/admin/pi-analysis/queue", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusTeapot)
			})

			h := admin.NewPIAnalysisHandler(nil, nil, nil, tc.pool, tc.secret)
			h.RegisterRoutes(mux, "/api/admin/pi-analysis")

			srv := httptest.NewServer(mux)
			defer srv.Close()

			resp, err := srv.Client().Get(srv.URL + "/api/admin/pi-analysis/queue")
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusTeapot {
				t.Errorf("鉴权材料缺失时端点**必须不被注册**（期望落到调用方自己的 418，"+
					"实际 %d）⇒ 现在它是可被访问的 ⇒ fail-closed 失效",
					resp.StatusCode)
			}
		})
	}
}

// TestPIAnalysisRoutesRegisteredOnMainMux 断言**主 mux 上真的注册了**。
//
// ★ 为什么上面那条 HTTP 测试**不够**：
//
//	它证明的是「admin.RegisterRoutes 注册出来的路由可达」，
//	但**不能**证明「main.go 调用了它」——
//	main.go 里完全可以没有这行调用，而 HTTP 测试照样全绿。
//	⇒ 这里补上「main.go 的接线点」这一半。
func TestPIAnalysisRoutesRegisteredOnMainMux(t *testing.T) {
	src := readMain(t) // 剥掉注释与字符串字面量的 main.go 源码

	if !strings.Contains(src, "admin.NewPIAnalysisHandler") {
		t.Fatal("main.go 里没有 admin.NewPIAnalysisHandler —— PI 智能体管理 API 没有被接线")
	}

	idx := strings.Index(src, "admin.NewPIAnalysisHandler")
	region := src[max(0, idx-400):min(len(src), idx+400)]

	// ★ 断言必须**指名道姓**：只查 "RegisterRoutes(mux)" 会**误判为绿** ——
	//
	//	同一段里有 adminHandler.RegisterRoutes(mux) /
	//	promptInjectionHandler.RegisterRoutes(mux) 等**同款调用**
	//	⇒ 哪怕 piAnalysisHandler.RegisterRoutes(mux) 被整行删掉，
	//	  邻近的同款调用仍然让 "RegisterRoutes(mux)" 出现在窗口里
	//	  ⇒ 门变绿，而端点其实已经 404。
	//	★ 这条正是**变异测试 M21 抓出来的**（首轮 M21 没红）。
	if !strings.Contains(region, "piAnalysisHandler.RegisterRoutes(mux") {
		t.Errorf("piAnalysisHandler 没有被 RegisterRoutes(mux…) 注册到 main mux：\n%s", region)
	}

	// ★ 注册**不得**依赖 PI 智能体功能开关。
	//
	//	背景：采集/分析默认关闭（会把攻击者原文落库），
	//	但**管理面必须一直在**（否则运维看到 404 而不是「队列为空」，
	//	「没开采集」与「采集了但队列为空」在运行期不可区分）。
	//
	//	★ 诚实边界：这是一条**拼写级**判据 —— 它只能认出
	//	  `piErrorCollectorGlobal != nil && piErrorAnalyzerGlobal != nil` 这一种写法。
	//	  真正的行为保证在 TestPIAnalysisRoutesReachableOverHTTP，
	//	  但那条测不到 main.go 的接线 ⇒ 两者互补，都留着。
	if strings.Contains(src, "piErrorCollectorGlobal != nil && piErrorAnalyzerGlobal != nil") {
		t.Error("main.go 里出现了 `piErrorCollectorGlobal != nil && piErrorAnalyzerGlobal != nil` —— " +
			"PI 管理 API 又被绑回「采集器与分析器都非 nil」这个功能开关上了。" +
			"只开采集（不开分析）时端点会 404 ⇒ 运维看不到队列。")
	}
}
