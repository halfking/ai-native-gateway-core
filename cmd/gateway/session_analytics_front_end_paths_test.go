package main

// 会话分析前端 API 路径 × 后端真实路由的**交叉对拍**钉测。
//
// ══════════════════════════════════════════════════════════════════════
// 为什么这道门存在（L34 十轮 · 2026-11-24）
//
// ★ 会话分析 E2E 的**离线半**有两层，第二层此前也缺位：
//
//	第一层（锚点存在性）—— L34 九轮补上：
//	    「flow 断的 id 锚点必须在前端树里真实存在」
//
//	第二层（★ 本文件 ★）：「★ 前端实际发出的 API 路径必须后端真的提供 ★」
//
// ★★ 实测查清的两处结构性缺口：
//
//	① **前端侧**：★ `web/src/api/sessionAnalytics.test.ts` 是一份**很认真**的
//	   URL 契约锁（★ 它明确写着「这些断言派生自 Go handlers」 ★★）
//	   ⇒ ★★ 但它断言的是**手抄的 URL 字面量** ⇒ ★★ **抄错了没有任何东西会红**
//	   ⇒ ★★ 而「抄」这件事本身就是漂移源 ★★
//
//	② **后端侧**：★ `admin/session_analytics/handlers.go` 的 `RoutePaths`
//	   只有 **6 条静态路由**（★ 可达性钉测 `TestRegisterRoutesAreReachable`
//	   就只跑这 6 条 ★★）⇒ ★★ 而前端实际调用的是 **11 条**（★ 含 5 条
//	   **动态子路由**：★ `/user-ranking/:owner` / `…/projects` /
//	   `…/projects/:project/tasks` / `…/models` / `…/api-keys` ★★）★
//	   ⇒ ★★ 那 5 条**子路由没有任何可达性钉测** ★★
//
// ★★ 两边都绿，**而它们之间没有交集校验** ⇒ ★★ 任一侧改了路径
//   （★ 前端改 URL、★ 后端改注册 ★）⇒ ★★ 另一侧不会红 ⇒ ★★
//   ⇒ ★★ **运行时 404** ⇒ 页面报数据错误 ⇒ ★★ **只有真机 E2E 才能发现** ★★
//   ⇒ ⇒ 而真机 E2E 不在 CI 里 ⇒ ⇒ **CI 上今天是零保护** ★★
//
// ★★ 为什么不用「手抄清单」再对一次（★ 那样只会多一份会漂移的清单 ★★）：
//   本门**从两个源头各自抽事实**、再求交集：
//     · 前端：**实际发出的 URL 模板**（★ 从 TS 源码里抽、★ 不是手挑 ★★）
//     · 后端：**生产同形 mux**（★ 把仓内真实注册路径逐条注册进去 ★★）
//   ⇒ ★★ 读数直接来自**真实 mux 的路由结果**，不是文本比对 ★★
//
// ★★ 关键口径（★ 承 L34 九轮的「存在性 ≠ 可命中性」同款纪律 ★★）：
//   · **404** ＝ 没有任何注册模式匹配 ⇒ ★★ 前端这条调用在后端不存在
//   · **418** ＝ 命中了**别人的 handler**（★ 本测试用 418 标记「非本包的
//     handler」 ★★）⇒ 对**本包拥有的**路径就是**被抢走** ⇒ 假绿风险
//   · **403** ＝ 命中了本包 handler、只是没有凭据 ⇒ ★ 这才是「可达」的证据 ★
//
// ★★ 归属判定**不靠手抄清单、也不靠正则**：★ 用 Go 的
//   `http.ServeMux.Handler(req)` ⇒ 它直接返回**匹配到的模式字符串** ★★
//   ⇒ ★★ 「这条路径归不归会话分析包管」是**运行时的既成事实** ★★
//
// ★★ 通用式：「两边的契约锁各自都绿」不等于「两边还对得上」——
//   ★★ 只要两份锁是**各抄各的**，它们就可能一起绿着一个已经坏掉的契约 ★★。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	sessionanalytics "github.com/kaixuan/llm-gateway-go/admin/session_analytics"
)

// sessionAnalyticsPrefix 是本门守的那一族 API 前缀。
const sessionAnalyticsPrefix = "/api/admin/session-analytics"

// genericCatchAll 是家族里的**兜底子树**（★ main.go 注册 ★★）。
//
// ★★ 它让「404 ⇒ 这条路径没人提供」这条推理**失效**：★ 任何多段路径都能落进它
//
//	⇒ ⇒ 因此判据必须直接看「谁接住了」，★ 而不是只看状态码 ★★。
const genericCatchAll = sessionAnalyticsPrefix + "/"

// frontEndTemplateRE 抓 TS 源码里的模板串（★ 反引号包裹 ★）。
var frontEndTemplateRE = regexp.MustCompile("`[^`]*`")

// stringLiteralRE 抓 TS 源码里的单/双引号字面串。
var stringLiteralRE = regexp.MustCompile(`'([^'\n]*)'|"([^"\n]*)"`)

// baseConstRE 抓「基址常量」的定义：★ `const NAME = '/api/admin/session-analytics'` ★。
var baseConstRE = regexp.MustCompile(`const\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*['"` + "`" + `]` + regexp.QuoteMeta(sessionAnalyticsPrefix) + `['"` + "`" + `]`)

// encodeSegmentRE 抓路径段里的编码表达式（`${encodeURIComponent(owner)}`）。
var encodeSegmentRE = regexp.MustCompile(`\$\{encodeURIComponent\([^}]*\)\}`)

// bareSegmentRE 抓路径段里的裸标识符（`${clientId}`）。
var bareSegmentRE = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

// frontEndAPIPath 一条从前端源码抽出来的请求路径。
type frontEndAPIPath struct {
	// RelPath 相对 `/api/admin/session-analytics/` 的路径（★ 占位符已换成实值 ★）。
	RelPath string
	// Source 抽取来源（文件:行），★ 报错时要能指到具体那行 ★。
	Source string
}

// stripPathPlaceholders 把一条模板串里的**路径部分**取出来：
//
//	① 先把 `${encodeURIComponent(…)}` 换成哨兵（★ 它是**路径段** ★★）；
//	② 再在**第一个其余的 `${`** 处截断（★ 那是查询串构造器、★ 不是路径 ★★）；
//	③ 最后把哨兵换成实值 `probe`。
//
// ★ 顺序不能换：★ 查询串里也可能出现同名键（★ `owner` ★★）
//
//	⇒ ★ 若先替换 `${…}` 再截断，★ 查询串里的 `owner` 会被一起换掉 ★★。
func stripPathPlaceholders(tmpl string) string {
	var b strings.Builder
	i := 0
	for i < len(tmpl) {
		// ① 查询串起点（`?`）⇒ 路径到此为止。
		if tmpl[i] == '?' {
			break
		}
		if strings.HasPrefix(tmpl[i:], "${") {
			end := strings.Index(tmpl[i:], "}")
			if end < 0 {
				break
			}
			expr := tmpl[i+2 : i+end]
			isCall := strings.Contains(expr, "(")
			// ★ 路径段 ⇒ 前一个字符是 `/`（★ 或是路径开头 ★★）。
			prevIsSlash := b.Len() > 0 && strings.HasSuffix(b.String(), "/")
			switch {
			case isCall && strings.HasPrefix(expr, "encodeURIComponent"):
				// ★ 路径段的编码表达式 ⇒ 实值。
				b.WriteString("probe")
			case isCall:
				// ★ 查询串构造器（`${query({…})}` / `${qs({…})}`）⇒ 路径到此为止。
				return b.String()
			case prevIsSlash:
				// ★ 路径段里的裸标识符（`${clientId}`）⇒ 实值。
				b.WriteString("probe")
			default:
				// ★ 裸标识符但**不在**路径段位置（★ 如 `clients${clientsQs}` ★）
				//   ⇒ 它是**查询串** ⇒ 路径到此为止。
				return b.String()
			}
			i += end + 1
			continue
		}
		b.WriteByte(tmpl[i])
		i++
	}
	return b.String()
}

// collectFrontEndAPIPaths 从前端 TS 源码里抽出所有实际发出的会话分析请求路径。
//
// ★ 扫描面：`web/src/api/*.ts` 全量（★ 不是只扫 sessionAnalytics.ts ——
//
//	★ 前端哪天把某个调用挪到别的文件里，这条门仍要看得见 ★★）。
//
// ★ 只认会话分析这一族（★ 本门只守会话分析域 ★）。
func collectFrontEndAPIPaths(t *testing.T) []frontEndAPIPath {
	t.Helper()

	root, err := repoRootForRouteGuard(t)
	if err != nil {
		t.Fatalf("定位仓库根目录失败: %v", err)
	}
	apiDir := filepath.Join(root, "web", "src", "api")

	entries, err := os.ReadDir(apiDir)
	if err != nil {
		t.Fatalf("读前端 API 目录失败 %s: %v", apiDir, err)
	}

	var out []frontEndAPIPath
	seen := map[string]bool{}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		// ★ 排除测试文件（★ 测试里出现的 URL 不是产品调用 ★★）。
		if strings.HasSuffix(e.Name(), ".test.ts") || strings.HasSuffix(e.Name(), ".spec.ts") {
			continue
		}
		path := filepath.Join(apiDir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", path, err)
		}
		rel, _ := filepath.Rel(root, path)
		text := string(src)

		// ★ 本文件里指向会话分析的基址常量名（★ 逐文件解析 ⇒ 不会串到别的族 ★★）。
		baseNames := map[string]bool{}
		for _, m := range baseConstRE.FindAllStringSubmatch(text, -1) {
			baseNames[m[1]] = true
		}

		emit := func(relPath string, idx int) {
			relPath = strings.Trim(relPath, "/")
			if relPath == "" {
				return
			}
			if seen[relPath] {
				return
			}
			// ★★ **注释里的路径不是调用**（★ 仓内大量注释在解释路由 ★★）
			//   ⇒ ★ 命中行若整行是注释 ⇒ 跳过（★ 否则一条文档改动会造成假红 ★★）。
			lineStart := strings.LastIndex(text[:idx], "\n") + 1
			lineText := strings.TrimSpace(text[lineStart:idx])
			if strings.HasPrefix(lineText, "//") || strings.HasPrefix(lineText, "*") ||
				strings.HasPrefix(lineText, "/*") {
				return
			}
			seen[relPath] = true
			line := 1 + strings.Count(text[:idx], "\n")
			out = append(out, frontEndAPIPath{
				RelPath: relPath,
				Source:  fmt.Sprintf("%s:%d", rel, line),
			})
		}

		// ① 模板串：★ 形如 `${BASE}/xxx` ★。
		for _, m := range frontEndTemplateRE.FindAllStringIndex(text, -1) {
			body := text[m[0]+1 : m[1]-1] // 去掉两侧反引号
			for name := range baseNames {
				marker := "${" + name + "}/"
				if !strings.Contains(body, marker) {
					continue
				}
				rest := body[strings.Index(body, marker)+len(marker):]
				emit(stripPathPlaceholders(rest), m[0])
			}
			// ①b 模板串：★ 形如 `/api/admin/session-analytics/xxx`（★ 直接写全前缀 ★★）。
			//   ★ 这一支覆盖 `web/src/api/admin.ts` 那族（★ 用户画像 / 客户端 / 任务 ★★）
			//   ⇒ ★ 它们**不归**会话分析包管（★ 属 admin/handler.go ★★）⇒ 只要求「不是 404」 ★★。
			if i := strings.Index(body, sessionAnalyticsPrefix+"/"); i >= 0 {
				rest := body[i+len(sessionAnalyticsPrefix)+1:]
				emit(stripPathPlaceholders(rest), m[0])
			}
		}

		// ② 普通字面串：★ 形如 '/api/admin/session-analytics/xxx' ★。
		for _, m := range stringLiteralRE.FindAllStringSubmatchIndex(text, -1) {
			// ★ 两个捕获组（单引号 / 双引号）⇒ 只有一个有效 ★。
			raw := ""
			if m[2] >= 0 {
				raw = text[m[2]:m[3]]
			} else if m[4] >= 0 {
				raw = text[m[4]:m[5]]
			} else {
				continue
			}
			if !strings.HasPrefix(raw, sessionAnalyticsPrefix+"/") {
				continue
			}
			rest := raw[len(sessionAnalyticsPrefix)+1:]
			if i := strings.IndexAny(rest, "?#"); i >= 0 {
				rest = rest[:i]
			}
			emit(rest, m[0])
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out
}

// TestSessionAnalyticsFrontEndPathsAreRoutable 是本门的主判据：
// ★ 前端实际发出的每条会话分析 API 路径，在**生产同形 mux**上必须真的能路由到。
func TestSessionAnalyticsFrontEndPathsAreRoutable(t *testing.T) {
	paths := collectFrontEndAPIPaths(t)

	// ★ 恒绿护栏：★ 抽不到路径 ⇒ 门会「一个都不检查」就判绿。
	if len(paths) < 5 {
		t.Fatalf("★ 只从前端抽到 %d 条会话分析 API 路径（≥ 5）⇒ 抽取面失效 ⇒ "+
			"★ 这道门会「一个都不检查」就判绿", len(paths))
	}

	// ★ 本包自己的 mux（★ 只注册本包路由 ★）⇒ 用它判定「这条路径归不归本包管」。
	//   ★ `ServeMux.Handler(req)` 直接返回**匹配到的模式字符串** ★★
	//   ⇒ ⇒ 归属是**运行时既成事实**，★ 不是手抄清单、也不是正则 ★★。
	pkgMux := http.NewServeMux()
	sessionanalytics.RegisterRoutes(pkgMux, nil, func(h http.HandlerFunc) http.HandlerFunc { return h })

	// ★ 生产同形 mux：既有路由先注册（★ stub 返回 418 ⇒ 标记「别人的 handler」 ★★）。
	mux := http.NewServeMux()
	for _, r := range collectRegisteredRoutePaths(t) {
		mux.HandleFunc(r.Pattern, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
	}
	// ★ 本包路由（★ 与生产同一份注册函数 ★★）。
	sessionanalytics.RegisterRoutes(mux, nil, func(h http.HandlerFunc) http.HandlerFunc { return h })

	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, p := range paths {
		p := p
		t.Run(p.RelPath, func(t *testing.T) {
			full := sessionAnalyticsPrefix + "/" + p.RelPath
			req := httptest.NewRequest(http.MethodGet,
				full+"?start_date=2026-01-01&end_date=2026-01-31", nil)

			// ★★★ 判据的三个事实（★ 全部来自 **ServeMux 的匹配结果** ★★）：
			//
			//	① 生产同形 mux 上**谁**接住了这条路径（★ pattern 串 ★★）
			//	② 会话分析包**自己**的 mux 上能不能接住（★ pattern 串 ★★）
			//	③ 真发一次 ⇒ 状态码
			//
			// ★★ 为什么必须有 ① 的 pattern 而不只是状态码：
			//   家族里有一条**兜底子树** `/api/admin/session-analytics/`
			//   （★ main.go 注册 ★★）⇒ ★★ **它让 404 检测失效**：
			//   任何多段路径都能落进它 ⇒ ⇒ 「404 ⇒ 这条路径没人提供」这条推理
			//   在有兜底子树时**不成立** ★★ ⇒ ⇒ 必须直接看「谁接住了」。
			//
			// ★★ 而「落进兜底子树」本身就是**缺陷**：★ 兜底对多段路径返回
			//   「unknown session-analytics endpoint」（404）、★ 对单段路径
			//   **误当作会话详情**（★ 打到别的端点、★ 返回 200/500 ★★）★★
			//   ⇒ ⇒ 两种都是「前端调的东西后端没有」。
			_, prodPattern := mux.Handler(req)
			_, pkgPattern := pkgMux.Handler(req)

			if prodPattern == "" {
				t.Errorf("★ 前端 sessionAnalytics 调用的路径 %s 在后端**没有任何路由接住**（404）\n"+
					"  ⇒ 来源：%s\n"+
					"  ⇒ 前端这一调用在运行时必然 404 ⇒ 页面报数据错误 ⇒ "+
					"★ **只有真机 E2E 才能发现**，而真机 E2E 不在 CI 里\n"+
					"  ⇒ 要么改前端、★ 要么改后端（★ 别只改一边 ★）",
					full, p.Source)
				return
			}
			if prodPattern == genericCatchAll {
				t.Errorf("★ 前端 sessionAnalytics 调用的路径 %s **落进了家族兜底子树** %s\n"+
					"  ⇒ 来源：%s\n"+
					"  ⇒ 兜底对多段路径回「unknown session-analytics endpoint」（404）、\n"+
					"  ⇒   对单段路径**误当作会话详情**（★ 打到别的端点 ★★）\n"+
					"  ⇒ ⇒ 两种都是「前端调的东西后端没有专门提供」",
					full, genericCatchAll, p.Source)
				return
			}
			if pkgPattern != "" && pkgPattern != prodPattern {
				t.Errorf("★ 前端 sessionAnalytics 调用的路径 %s 在生产同形 mux 上被**别人接走**\n"+
					"  ⇒ 本包自己的 mux 会用模式 %s 接住它、★ 而生产同形 mux 用的是 %s\n"+
					"  ⇒ 来源：%s",
					full, pkgPattern, prodPattern, p.Source)
				return
			}

			// 真发一次 ⇒ 状态码。
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code == http.StatusTeapot {
				// ★ 418 = 命中了**别人**的 handler（★ 本测试的 stub 标记 ★★）
				//   ⇒ ★ 走到这里说明上面三条都没判出问题（★ 路径由别家提供 ★★）
				//   ⇒ ★ **本会话分析域不归它管** ⇒ 不判红（★ 只登记 ★★）。
				t.Logf("ℹ️ 路径 %s 由别家提供（模式 %s）⇒ 不属会话分析包 ⇒ 只验「有路由」", full, prodPattern)
				return
			}
			if pkgPattern != "" && rec.Code != http.StatusForbidden {
				// ★ 3xx 单独说：★ Go 的 ServeMux 对「**只有子树模式**」的裸路径
				//   会回 **307 跳转**（★ `/a/b/` ⇒ `/a/b` ★★）⇒ ⇒ 这说明
				//   **精确路由没了、只剩子树** ⇒ ★★ 生产上前端会先吃到跳转、
				//   跟到子树后落到**别的 handler 或 404** ⇒ ⇒ 同样是契约破了 ★★。
				if rec.Code >= 300 && rec.Code < 400 {
					t.Errorf("★ 前端 sessionAnalytics 调用的路径 %s 只被**子树模式**接住（%d 跳转）\n"+
						"  ⇒ 精确路由没有注册 ⇒ ★★ 生产上前端会先吃跳转、跟到子树后\n"+
						"  ⇒   落到**别的 handler 或 404** ⇒ ⇒ 同样是「前端调的东西后端没有」\n"+
						"  ⇒ 来源：%s",
						full, rec.Code, p.Source)
					return
				}
				t.Errorf("★ 前端 sessionAnalytics 调用的路径 %s 期望 403（命中本包 handler、无凭据被权限检查拒绝），实际 %d\n"+
					"  ⇒ 来源：%s",
					full, rec.Code, p.Source)
			}
		})
	}
}

// TestSessionAnalyticsFrontEndPathExtractionHasTeeth 是**量具自检**：
// ★ 抽取器必须真的认得出路径（★ 否则主判据会「一个都不检查」就判绿 ★★）。
//
// ★ 判据是**形态**而不是数量：★ 喂进一个仓内形态的路径 ⇒ 抽取器必须
//
//	**原样吐出来**（★ 证明它不是「什么都没抽到」 ★★）。
func TestSessionAnalyticsFrontEndPathExtractionHasTeeth(t *testing.T) {
	// ① 路径段占位符 ⇒ 实值（★ 且**只**替换路径段 ★★）。
	got := stripPathPlaceholders("user-ranking/${encodeURIComponent(owner)}/projects")
	want := "user-ranking/probe/projects"
	if got != want {
		t.Fatalf("★ 路径段占位符替换失灵：得到 %q，期望 %q", got, want)
	}
	// ② 查询串必须先被截掉（★ 否则查询串里的 `owner` 会被一起换掉 ★★）。
	got = stripPathPlaceholders("user-ranking/${encodeURIComponent(owner)}${query({ owner: params.owner })}")
	want = "user-ranking/probe"
	if got != want {
		t.Fatalf("★ 查询串截取失灵：得到 %q，期望 %q", got, want)
	}
	// ③ 基址常量必须逐文件解析（★ 否则会串到别的 API 族 ★★）。
	if !baseConstRE.MatchString("const BASE = '/api/admin/session-analytics'") {
		t.Fatal("★ 基址常量的识别正则失灵 ⇒ 会把别的 API 族也抽进来 ⇒ 假红源")
	}
	if baseConstRE.MatchString("const BASE = '/api/admin/error-playbook'") {
		t.Fatal("★ 基址常量的识别正则**过宽** ⇒ 会把别的 API 族也抽进来 ⇒ 假红源")
	}
}
