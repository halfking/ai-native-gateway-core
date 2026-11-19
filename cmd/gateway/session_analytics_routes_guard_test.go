package main

// 会话分析系统（981 族）路由的**冲突守卫 + 可达性**钉测。
//
// ══════════════════════════════════════════════════════════════════════
// 2026-11-20 211 部署事故的第一性根因（真·生产证据，不是推演）：
//
//	新端点按设计文档写成 `/api/admin/session-analytics/users`，
//	而仓内**早就有**同名路径且**前端在用**：
//	  admin/handler.go:1334
//	    mux.HandleFunc("/api/admin/session-analytics/users",
//	                    admin(h.handleUserAnalyticsList))   // 2026-07-07 用户画像
//
//	⇒ 同一个 ServeMux 上**同一路径注册两次**
//	⇒ Go 的 stdlib ServeMux 直接 panic：
//	    "pattern \"/api/admin/session-analytics/users\" ... conflicts with pattern ..."
//	⇒ cmd/gateway/main.go 的 panic 兜底把它变成「启动失败 ⇒ 进程退出(1)」
//	⇒ 蓝绿候选槽起不来 ⇒ 部署脚本 120s 就绪探测失败 ⇒ **回退**
//	⇒ 线上永远拿不到新端点（且**零流量影响**，旧槽一直在服务）。
//
//	★ 生产日志（211 容器实拍）：
//	    "msg":"main: panic during startup",
//	    "panic":"pattern \"/api/admin/session-analytics/users\" ...
//	              (registered at /app/admin/session_analytics/handlers.go:357)
//	              conflicts with pattern ... (registered at /app/admin/handler.go:1334)"
//
// ★★ 为什么本仓上一版的钉测**打不穿**（本轮最贵的一课）：
//
//	上一版钉测在**空 mux** 上注册自己的路由，再断言"能被路由到"。
//	空 mux 上**根本不存在**任何冲突 ⇒ 这条测试与「生产上会不会炸」**零相关**。
//	⇒ 通用式：**可达性钉测必须在"生产同形的 mux"上做**；
//	  "我在自己造的干净环境里能跑" ≠ "我放进生产那个真环境还能跑"。
//
// ★ 本文件的两层守卫：
//
//	1. TestSessionAnalyticsRoutesDoNotCollideWithRegisteredRoutes —— 钉「静态」：
//	   用 go/ast 扫**全仓**已注册的路由字面量，与本包的 RoutePaths 比对，
//	   有交集即红（撞车 = 启动 panic）。
//	2. TestSessionAnalyticsRoutesOnProductionShapedMux —— 钉「运行时」：
//	   把扫出来的既有路径**真的注册**到同一个 mux（生产同形），
//	   再注册本包的路由 ⇒ **必须不 panic**；并真发 HTTP 断言请求
//	   命中的是**本包的 handler**（403 无凭据）而不是别人的 stub（418）。
// ══════════════════════════════════════════════════════════════════════

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	sessionanalytics "github.com/kaixuan/llm-gateway-go/admin/session_analytics"
)

// registeredRoutePath 是一条从**源码**里抽出来的路由注册记录。
type registeredRoutePath struct {
	Pattern string
	File    string
}

// collectRegisteredRoutePaths 扫出仓内**已注册**的路由路径字面量。
//
// ★ 为什么用 go/ast 而不是正则：
//
//	正则会把**注释里**写的路径也算进来（仓内大量注释在解释路由），
//	也会把**非路由**的 Handle 调用算进来。
//	⇒ AST 只认真正的 CallExpr + 字符串字面量首参 ⇒ 与运行时行为一致。
//
// ★ 为什么排除 admin/session_analytics/：
//
//	那是**本包自己**的注册点 —— 它注册的就是我们要比对的那几条，
//	算进来就是自己跟自己撞车。
func collectRegisteredRoutePaths(t *testing.T) []registeredRoutePath {
	t.Helper()

	root, err := repoRootForRouteGuard(t)
	if err != nil {
		t.Fatalf("定位仓库根目录失败: %v", err)
	}

	var out []registeredRoutePath
	seen := map[string]bool{}
	fset := token.NewFileSet()

	for _, dir := range []string{
		filepath.Join(root, "admin"),
		filepath.Join(root, "cmd", "gateway"),
	} {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if info.IsDir() {
				base := filepath.Base(path)
				if base == "vendor" || base == "testdata" || base == "node_modules" {
					return filepath.SkipDir
				}
				// 本包自己注册的那几条：由 RoutePaths 单独比对。
				if filepath.Base(path) == "session_analytics" &&
					filepath.Base(filepath.Dir(path)) == "admin" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if !fileBuildsOnThisPlatform(t, path) {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("解析 %s 失败: %v", path, err)
			}
			rel, _ := filepath.Rel(root, path)
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc" {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				pattern, err := strconv.Unquote(lit.Value)
				if err != nil || pattern == "" {
					return true
				}
				if seen[pattern] {
					return true
				}
				seen[pattern] = true
				out = append(out, registeredRoutePath{Pattern: pattern, File: rel})
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("扫描 %s 失败: %v", dir, err)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Pattern < out[j].Pattern })
	return out
}

// fileBuildsOnThisPlatform 判断该文件在**当前平台**是否参与编译。
//
// ★ 为什么需要：带 `//go:build` 约束（或 _linux.go 之类后缀）的文件
//
//	可能在本平台**不编译**，它的注册就不会真的发生
//	⇒ 不该算进冲突集（否则假红）。
//
// ★ 用 build.Context.MatchFile 而不是手搓约束求值：
//
//	它是 go 工具链自己的判定口径（含 OS/arch 后缀、ignore tag 等），
//	与"真正会不会被编译进二进制"一致。
func fileBuildsOnThisPlatform(t *testing.T, path string) bool {
	t.Helper()
	ok, err := build.Default.MatchFile(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return false
	}
	return ok
}

// repoRootForRouteGuard 从 cmd/gateway 往上找仓库根（go.mod 所在）。
func repoRootForRouteGuard(t *testing.T) (string, error) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", os.ErrNotExist
}

// TestSessionAnalyticsRoutesDoNotCollideWithRegisteredRoutes 静态守卫：
// 本包的端点路径不得与仓内**任何**已注册路径重名。
//
// ★ 重名 = 同一个 ServeMux 上重复注册 = panic = 整个网关启动失败。
//
//	这不是"某个端点 404"，是**进程起不来**（211 部署实测）。
func TestSessionAnalyticsRoutesDoNotCollideWithRegisteredRoutes(t *testing.T) {
	existing := collectRegisteredRoutePaths(t)
	index := map[string][]string{}
	for _, r := range existing {
		index[r.Pattern] = append(index[r.Pattern], r.File)
	}

	for _, p := range sessionanalytics.RoutePaths {
		if files, ok := index[p]; ok {
			t.Errorf("★ 会话分析端点 %s 与仓内既有注册路径重名：%v\n"+
				"  ⇒ 同一 ServeMux 上重复注册 ⇒ ServeMux panic ⇒ **整个网关启动失败**\n"+
				"  ⇒ 蓝绿部署的候选槽起不来 ⇒ 部署回退 ⇒ 新端点永远上不了线\n"+
				"  ⇒ 改路径（别去动既有的那条：它可能有前端在用）",
				p, files)
		}
	}
}

// TestSessionAnalyticsRoutesOnProductionShapedMux 运行时守卫：
// 在**生产同形**的 mux 上注册本包路由，必须不 panic，且请求命中本包 handler。
//
// ★ 「生产同形」＝ 把仓内扫出来的既有路径**逐条真的注册**到同一个 mux。
//
//	上一版钉测用空 mux ⇒ 撞不到任何东西 ⇒ 这正是漏网的根因。
func TestSessionAnalyticsRoutesOnProductionShapedMux(t *testing.T) {
	mux := http.NewServeMux()

	// 生产同形：既有路由先注册（stub 返回 418，用来识别"被别人接走"）。
	for _, r := range collectRegisteredRoutePaths(t) {
		pattern := r.Pattern
		mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot) // 418 = 这是别人的 handler
		})
	}

	// ★ 本包的注册。历史上这里会 panic（重复注册 /users）。
	//   panic 会让**整个网关**退出 ⇒ 部署回退 ⇒ 端点永不可达。
	sessionanalytics.RegisterRoutes(mux, nil, func(h http.HandlerFunc) http.HandlerFunc { return h })

	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, p := range sessionanalytics.RoutePaths {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet,
				p+"?start_date=2026-01-01&end_date=2026-01-31", nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code == http.StatusNotFound {
				t.Fatalf("端点 %s 返回 404 ⇒ 路由没注册或被前缀路由吞掉", p)
			}
			if rec.Code == http.StatusTeapot {
				t.Fatalf("端点 %s 命中了别人的 handler（418）⇒ 被既有路由抢走", p)
			}
			// 无凭据 ⇒ 本包 handler 的权限检查拒绝（403）。
			// 其它状态码说明请求没走到本包 handler。
			if rec.Code != http.StatusForbidden {
				t.Errorf("端点 %s：期望 403（无凭据被本包权限检查拒绝），实际 %d",
					p, rec.Code)
			}
		})
	}
}
