// Package ingressguard 钉住「入站数据面覆盖面」的登记表。
//
// R89-EH（218 号）为什么要有这个包：
//
// 217 号坐实了一件对 79 号裁决至关重要的事 —— **配额闸（CheckBudget）的覆盖面
// 不是全有也不是全无**：`/v1/chat/completions`、`/v1/completions`、`/v1/embeddings`、
// 音频三面、Gemini 两个路径有；`/v1/messages`、`/v1/responses` **没有**。
// 218 号又查出 `/v1/messages/count_tokens` **既没有配额闸、也没有 RPM 限流**
// （它不调上游所以没有配额闸是合理的，但它 `io.ReadAll` 整个 body 后做
// token 估算，而它自己的 ServeHTTP 里没有 `checkGatewayRateLimit`，
// 而同级 `/v1/messages` 在 `messages.go:429` 是有的）。
//
// ⚠️ 补两道闸**都会改变对外行为**（402 / 429），属产品裁决 ⇒ 本包**不改行为**，
// 只把现状**钉成可执行登记表**。
//
// ── 为什么要单独建包，而不是把门放进 domains/streaming ──────────────────
// 217 号留下的顺位是「先把 domains/streaming 纳入 GUARD_PACKAGES 再加覆盖面门」。
// 218 号**先量了预算**，结论是**它进不去**：
//
//	实测 `go test ./domains/streaming/ -count=1 -timeout=600s` → **ok 106.471s**（第 1 次）
//	与 **ok 107.204s**（第 2 次），墙钟 110s / 111.8s，
//	而 `make guards` 用的是 `-timeout=120s`（`Makefile` 的 `guards` target）。
//	⇒ 余量只剩 **13.5 / 8.2 秒**，而 216 号已实测 `sql/schema` 在 **25s~83s** 之间波动
//	（同机、同样代码、相邻两次就差 42 秒）⇒ 环境相关的波动足以吃掉这点余量。
//	⇒ 登记它 = 给 CI 埋一个**只在慢机器上才炸**的雷。
//
// 所以本包刻意**不导入 domains/streaming**：它只做**静态文本判据**
// （读 cmd/gateway/main.go 的路由注册 + 各 handler 源文件里锚点字符串的出现次数），
// 因此编译与运行都是毫秒级，可以安全进 CI。
//
// ── 本门是「契约钉桩」，不是「检测活缺陷」──
// 它断言的是**现状**（含缺口）。缺口被如实登记，所以现在**是绿的**；
// 谁改了行为却不更新登记表，本门会转红，强制同步 —— 否则下一个接手的人
// 会把「已登记的缺口」当成「已覆盖」。（与 216 号对 provenance 接缝的处理同型。）
//
// 判据取**具体次数**（恰好 1 次 / 恰好 0 次）而不是「存在 / 不存在」，
// 这样任何一侧的真实变更都会看见，而不是恒亮（playbook §151 的教训）。
package ingressguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gateKind 说明一个数据面怎么拿到（或拿不到）某道闸。
type gateKind string

const (
	// gateOwn：该 handler 文件自身就有这一道闸的调用点。
	gateOwn gateKind = "own"
	// gateDelegated：不自己实现，但把请求交给某个已有该闸的 handler。
	// 必须填 Delegate，否则无法核对「被委派方确实有」。
	gateDelegated gateKind = "delegated"
	// gateNone：确实没有。必须填 Why，说明为什么这是可接受的（或为何待裁决）。
	gateNone gateKind = "none"
)

// planeKind 区分**推理面**与**控制面**。
//
// ⚠️ 第一版判据没有这一层，把 main.go 上所有 `/v1` 前缀都当成推理面，
// 于是门立刻报出 9 个「未登记」：/v1/models、/v1/sessions(+/)、/v1/gw/sessions(/)、
// /v1/goal-runs(+/)、/v1/hosted-tasks(/)。
// ⚠️ 但它们**不调上游、不消耗额度**（列模型、查会话、查目标运行状态、查托管任务）
// ⇒ 对它们要求配额闸是**错的判据**，不是真的缺口。
//
// 这就是 209 号判据返工的三版教训在新场景的复现：
// **判据取「路径前缀」会把「同名不同类」的对象混进同一个集合。**
// ⇒ 覆盖面下限仍然对**所有** `/v1` 路径生效（漏登记必须红）、
//
//	但**只有推理面**才被断言「闸的声明与代码一致」。
type planeKind string

const (
	// planeInference：会调用上游、消耗额度 ⇒ 必须对配额闸与限流表态。
	planeInference planeKind = "inference"
	// planeControl：不调上游（查询/管理）⇒ 只需登记，不要求闸。
	planeControl planeKind = "control"
)

type surface struct {
	// Plane 决定本条是否被断言闸的声明。必须显式写，不留零值。
	Plane planeKind
	// Routes 是 main.go 上注册的路径。门会核对它们确实出现在 main.go 里。
	Routes []string
	// HandlerVars 是这些路由在 main.go 里绑定的 handler 变量/表达式。
	// 门会核对每条路由的注册行里出现的是**其中之一**，且每个声明都被用上
	// （防止写一个永不匹配的死变量把断言架空）。
	// 这条是 218 号第三版补的：第一版只登记 File，于是「把 File 指向另一个
	// 也有 own 闸的文件」能让门继续绿 —— 登记表与 main.go 的接线是脱钩的。
	HandlerVars []string
	// File 是相对本包目录的 handler 源文件（"../../" 开头）。所有条目必填。
	File string
	// Budget = CheckBudget（累计配额，超了 402）。
	Budget gateKind
	// BudgetDelegate 是 Budget==gateDelegated 时的被委派方描述。
	BudgetDelegate string
	// BudgetWhy 是 Budget==gateNone 时的理由。
	BudgetWhy string
	// RateLimit = checkGatewayRateLimit（RPM 限流）。
	RateLimit gateKind
	// RateLimitWhy 是 RateLimit==gateNone 时的理由。
	RateLimitWhy string
}

// ⚠️ 这张表是 218 号在当前 HEAD（merge 后）逐项核对出来的。
// 新增入站面**必须**在此登记，否则 TestIngressSurfaceRegistryCoversEveryRoute 转红。
var registry = []surface{
	{
		Plane:       planeInference,
		Routes:      []string{"/v1/chat/completions", "/v1/completions"},
		HandlerVars: []string{"chatRouteHandler"},
		File:        "../../domains/streaming/handler.go",
		Budget:      gateOwn,
		RateLimit:   gateOwn,
	},
	{
		// 218 号：/v1/messages 有 promptBudgetExceeded（单请求 prompt 大小上限，
		// 防 245 号 OOM），但**没有** CheckBudget（租户累计配额）。
		// 这是待裁决 79 的核心：补它会让原本能通过的请求变 402。
		Plane:        planeInference,
		Routes:       []string{"/v1/messages"},
		HandlerVars:  []string{"messagesRouteHandler"},
		File:         "../../domains/streaming/messages.go",
		Budget:       gateNone,
		RateLimit:    gateOwn,
		BudgetWhy:    "待裁决 79：补它会把原本能通过的请求变 402，属对外行为变更，审计代理不代拍",
		RateLimitWhy: "",
	},
	{
		// 218 号新发现：该端点不调上游（本地 token 估算）⇒ 无配额闸是合理的；
		// 但它同样没有 RPM 限流，而同级 /v1/messages 有。
		// 它消耗的是网关自身算力：messages_count_tokens.go:64 的
		// `io.ReadAll(io.LimitReader(r.Body, maxBodySize+1))` 最多读 **128MB**
		// （maxBodySize 见 handler.go:69）再做一次 Anthropic 形状解析，
		// 且没有任何并发/频率上限；注释（:16-18）明写 Claude Code 在
		// **每个上下文管理周期**都会探测它 ⇒ 高频。
		// ⚠️ 限流在**各自 handler 自己的 ServeHTTP/serveHTTPInner 里**施加
		//   （全包非测试调用点恰好 5 处：handler.go:3236、messages.go:429、
		//   responses.go:422、embeddings.go:390、audio_service.go:697），
		//   而 CountTokensHandler 是**另一个类型**、自己的 ServeHTTP 里那道调用没有
		//   ⇒ 「同级有、自己没有」不是偶然，也不是 wrapper 漏包。
		Plane:        planeInference,
		Routes:       []string{"/v1/messages/count_tokens"},
		HandlerVars:  []string{"chatHandler"},
		File:         "../../domains/streaming/messages_count_tokens.go",
		Budget:       gateNone,
		RateLimit:    gateNone,
		BudgetWhy:    "不调上游、无上游花费 ⇒ 无累计配额闸是合理的",
		RateLimitWhy: "无 RPM 限流；单请求可读至 128MB（handler.go:69 的 maxBodySize）且无频率上限 ⇒ P2 缺口，待裁决是否补",
	},
	{
		Plane:        planeInference,
		Routes:       []string{"/v1/responses"},
		HandlerVars:  []string{"responsesRouteHandler"},
		File:         "../../domains/streaming/responses.go",
		Budget:       gateNone,
		RateLimit:    gateOwn,
		BudgetWhy:    "同 /v1/messages：待裁决 79",
		RateLimitWhy: "",
	},
	{
		Plane:       planeInference,
		Routes:      []string{"/v1/embeddings"},
		HandlerVars: []string{"embeddingsHandler"},
		File:        "../../domains/streaming/embeddings.go",
		Budget:      gateOwn,
		RateLimit:   gateOwn,
	},
	{
		// 三个端点共享同一个 AudioService ⇒ 闸在共享核心里。
		Plane:       planeInference,
		Routes:      []string{"/v1/audio/transcriptions", "/v1/audio/speech", "/v1/mcp"},
		HandlerVars: []string{"audioTranscriptionsHandler", "audioSpeechHandler", "audioMCPHandler"},
		File:        "../../domains/streaming/audio_service.go",
		Budget:      gateOwn,
		RateLimit:   gateOwn,
	},
	{
		// R49（2026-10-07）补登：2fb2ec53f 的 refine/analyze 转写后处理
		// （audioTransformHandler）。预算闸自有（authenticateTransform 内
		// CheckBudget，失败 fail-closed）；RPM 刻意不计：refine/analyze 的
		// LLM 步骤环回 chat 面时带同一把调用方 key，那一跳已计一次 RPM
		// ——此处再计即双计，默认 12/min 的周期任务会被打 429
		// （TestTransformEndpointDoesNotConsumeRPM 钉住单计口径）。
		Plane:       planeInference,
		Routes:      []string{"/v1/audio/refine", "/v1/audio/analyze"},
		HandlerVars: []string{"audioTransformHandler"},
		File:        "../../domains/streaming/audio_transform.go",
		Budget:      gateOwn,
		RateLimit:   gateNone,
		RateLimitWhy: "刻意单计：环回 chat 步带同一把调用方 key 已计一次 RPM，transform 端点自身不消耗（否则双计 429）；见 audio_transform.go 文件头与 TestTransformEndpointDoesNotConsumeRPM",
	},
	{
		// Gemini：它自己的 ServeHTTP 里两道闸都没有，但它把 body 转成 OpenAI
		// 形态后合成 path=/v1/chat/completions 的请求交给 chatHandler
		// （handler_gemini.go:330 ParseGemini -> :352 SerializeOpenAI
		//   -> :369/239 newGeminiSyntheticRequest -> :378/388 chatHandler.ServeHTTP）
		// ⇒ 闸由 chatHandler 施加。
		// ⚠️ 这一条正是 215 号与 217 号两轮各错一次的位置（一次错判「零脱敏」、
		// 一次错判「无配额闸」），所以它必须被显式登记成 delegated，
		// 否则下一个人还会把「它没有自己的闸」读成「它没有闸」。
		Plane:          planeInference,
		Routes:         []string{"/v1beta/models/", "/v1/models/"},
		HandlerVars:    []string{"geminiHandler"},
		File:           "../../domains/streaming/handler_gemini.go",
		Budget:         gateDelegated,
		BudgetDelegate: "chatHandler -> serveHTTPInner -> handler.go 的 CheckBudget",
		RateLimit:      gateDelegated,
		RateLimitWhy:   "同 Budget：同一个委派方",
	},

	// ── 控制面：不调上游、不消耗额度，故不要求配额闸/限流声明 ────────────
	// ⚠️ 第一版判据把它们当成推理面，门立刻报出「9 个未登记」——
	//   那不是缺口，是判据把**同名不同类**的对象混进了同一集合（209 号三版返工的重演）。
	//   覆盖面下限对它们**依然生效**：忘了登记仍会红。
	{
		Plane:        planeControl,
		Routes:       []string{"/v1/models"},
		HandlerVars:  []string{"modelsHandler"},
		File:         "../../domains/streaming/models.go",
		Budget:       gateNone,
		RateLimit:    gateNone,
		BudgetWhy:    "列模型元数据，不调上游 ⇒ 无累计配额闸",
		RateLimitWhy: "同上：纯查询面",
	},
	{
		Plane:        planeControl,
		Routes:       []string{"/v1/sessions", "/v1/sessions/", "/v1/gw/sessions", "/v1/gw/sessions/"},
		HandlerVars:  []string{"sessionHandler"},
		File:         "../../domains/session/handler.go",
		Budget:       gateNone,
		RateLimit:    gateNone,
		BudgetWhy:    "会话查询/管理面，不调上游 ⇒ 无累计配额闸",
		RateLimitWhy: "同上：纯查询面",
	},
	{
		Plane:        planeControl,
		Routes:       []string{"/v1/goal-runs", "/v1/goal-runs/"},
		HandlerVars:  []string{"goalRunHandler"},
		File:         "../../internal/handlers/goalrun_handler.go",
		Budget:       gateNone,
		RateLimit:    gateNone,
		BudgetWhy:    "目标运行状态查询面，不调上游 ⇒ 无累计配额闸",
		RateLimitWhy: "同上：纯查询面",
	},
	{
		Plane:        planeControl,
		Routes:       []string{"/v1/hosted-tasks", "/v1/hosted-tasks/"},
		HandlerVars:  []string{"htHandler"},
		File:         "../../domains/hostedtask/handler.go",
		Budget:       gateNone,
		RateLimit:    gateNone,
		BudgetWhy:    "托管任务状态查询面，不调上游 ⇒ 无累计配额闸",
		RateLimitWhy: "同上：纯查询面",
	},
	{
		// ⚠️ 218 号第二版判据才看见的这条路由：第一版解析器只认 `mux.Handle(`，
		// 而它是用 `mux.HandleFunc(` 注册的 ⇒ **门面在自己建起来当天就漏了一条真路由**。
		// 这正是 playbook §210「下限必须钉在宽集合上」的复现：
		// 当时的覆盖面下限量的是**解析器的输出**，解析器看不见的东西不会让它红。
		//
		// 归类：控制面。它确认 handoff 提案并把会话切到目标会话，不调上游；
		// 它自己的保护不是 RPM，而是领域内的 cooldown + handoff 预算
		// （handoff_confirmation.go:97-100 的 ErrConfirmationCooldownActive /
		//   ErrConfirmationBudgetExhausted 正是这两个错误的出口）
		// ⇒ 补 RPM 限流不是它的正确保护手段，故不登记为缺口。
		Plane:        planeControl,
		Routes:       []string{"/v1/handoffs/confirm"},
		HandlerVars:  []string{"chatHandler.HandleHandoffConfirmation"},
		File:         "../../domains/streaming/handoff_confirmation.go",
		Budget:       gateNone,
		RateLimit:    gateNone,
		BudgetWhy:    "只确认提案并切换会话，不调上游 ⇒ 无累计配额闸",
		RateLimitWhy: "不调上游；防重放/防刷由领域内 cooldown + handoff 预算承担（见本条注释），RPM 限流不是它的保护手段",
	},
}

const (
	budgetAnchor  = ".CheckBudget("
	rateAnchor    = "checkGatewayRateLimit("
	mainRouteFile = "../../cmd/gateway/main.go"
)

func readSource(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func countOf(src, needle string) int { return strings.Count(src, needle) }

// routeLine 是从 main.go 里解析出的一条路由注册。
type routeLine struct {
	// Path 是引号里的路径字面量。
	Path string
	// Handler 是路径之后剩下的部分（handler 变量/表达式），用于核对接线。
	Handler string
	// LineNo 是它在 main.go 里的行号，只用于报错可读性。
	LineNo int
}

// v1Literals 抽出 main.go 一行里所有以 /v1 开头的引号字面量。
// ⚠️ 取的是**全部** /v1 字面量（宽集合），而不是「我认识的注册形式」
// —— 这样「用了我没预料到的形式注册的路由」会落到下面 unclassified 分支转红，
// 而不会被静默漏掉（playbook §210）。
func v1Literals(line string) []string {
	var out []string
	for i := 0; i < len(line); i++ {
		if line[i] != '"' {
			continue
		}
		end := strings.IndexByte(line[i+1:], '"')
		if end < 0 {
			return out
		}
		lit := line[i+1 : i+1+end]
		if strings.HasPrefix(lit, "/v1") {
			out = append(out, lit)
		}
		i += end + 1
	}
	return out
}

func isRegistrationLine(line string) bool {
	return strings.Contains(line, "Handle(") || strings.Contains(line, "HandleFunc(")
}

// isNonRouteLine 判断一行是否**证明**它上面的 /v1 字面量不是路由。
// 218 号实测 main.go 里有 6 处这样的字面量，全部是这两种：
//   - `slog.Info(..., "path", "/v1/goal-runs/{id}", ...)`：只为日志好看而写的路径串，
//     真正的路由是 `/v1/goal-runs` 与 `/v1/goal-runs/`（前缀匹配子路径）。
//   - `loopback.GatewayBase() + "/v1"`：客户端拼 URL 用的基址。
//
// 刻意**不写例外清单**：清单会变成「以后懒得看了」的表。
// 改成「必须能被归类成这两类之一」，新增第三种形态就会红。
//
// ⚠️ insideLog 必须由调用方用**跨行**状态算出来，不能只看本行有没有 "slog."：
//
//	slog.Info("hosted task endpoints enabled",
//	  "paths", []string{"/v1/hosted-tasks"},   // ← main.go:6512，字面量在**续行**上
//	  ...)
//
// 本行既没有 slog. 也没有 Handle( ⇒ 第一版按行判定的版本立刻把它报成
// 「无法归类」（实测确实报了，并逼出这一次订正）。
func isNonRouteLine(line string, insideLog bool) bool {
	return insideLog || strings.Contains(line, "GatewayBase()")
}

// parseRoutes 解析 main.go 的所有 /v1 路由注册。
// 返回：路由表、无法归类的 /v1 字面量（及其行号与该行内容）。
func parseRoutes(t *testing.T, main string) (map[string][]routeLine, []string) {
	t.Helper()
	routes := map[string][]routeLine{}
	var unclassified []string

	lines := strings.Split(main, "\n")
	// 跨行状态：slog.Info(...) 可以跨多行，字面量常落在续行上。
	// ⚠️ 深度只数**圆括号**：`[]string{...}` 的花括号不计入，
	//   所以单行的 `slog.Info("x", "paths", []string{"/v1/sessions"})` 深度是 0
	//   ⇒ 必须额外用「本行含 slog.」来判定它是日志行，不能只看状态位。
	logActive, logDepth := false, 0
	for i, raw := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(raw)
		insideLog := logActive || strings.Contains(raw, "slog.")
		// 纯注释行不参与状态推进：注释里的 slog. 不该把后面的行拖进日志上下文。
		if !strings.HasPrefix(trimmed, "//") {
			if at := strings.Index(raw, "slog."); at >= 0 {
				logDepth = countParen(raw[at:])
			} else if logActive {
				logDepth += countParen(raw)
			}
			logActive = logDepth > 0
		}

		lits := v1Literals(raw)
		if len(lits) == 0 {
			continue
		}
		if !isRegistrationLine(raw) {
			if isNonRouteLine(raw, insideLog) {
				continue // 已分类为非路由，属预期
			}
			// 逐个字面量记一条：一次报全，不让「多了几个」这件事被行号淹没。
			for range lits {
				unclassified = append(unclassified,
					"main.go:"+itoa(lineNo)+"  "+strings.TrimSpace(raw))
			}
			continue
		}
		for _, path := range lits {
			idx := strings.Index(raw, `"`+path+`"`)
			if idx < 0 {
				continue
			}
			routes[path] = append(routes[path], routeLine{
				Path:    path,
				Handler: strings.TrimSpace(raw[idx+len(path)+2:]),
				LineNo:  lineNo,
			})
		}
	}
	return routes, unclassified
}

// countParen 是一段文本的圆括号净深度（开 - 闭）。
// 只数圆括号：Go 的复合字面量用花括号，不能拿它算嵌套。
func countParen(s string) int {
	return strings.Count(s, "(") - strings.Count(s, ")")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestIngressSurfaceRegistryCoversEveryRoute 是本包的覆盖面下限。
//
// 判据分三步，顺序本身就是防线：
//  1. **宽集合自证**：把 main.go 里每一个 /v1 字面量都归类成
//     「注册行」「日志串」「客户端基址」三者之一。出现第四类 ⇒ 转红。
//     这一步是为了让「解析器不认识某种注册写法」变成**硬红**而不是静默漏掉。
//     （218 号第二版就是在这一步发现第一版漏了 `mux.HandleFunc` 注册的
//     /v1/handoffs/confirm —— 门面自己建起来当天就漏了一条真路由。）
//  2. **下限非空**：一条路由都没解析出来 ⇒ 解析器坏了，下限会变成空断言 ⇒ 转红。
//  3. **登记表覆盖**：每条解析出的路由都必须在 registry 里恰好登记一次。
//
// 取「登记表覆盖 main.go 已注册的数据面」这个方向，而不是反向，
// 是因为反向（登记表里的每条都在 main.go 出现）无法发现**新增路由漏登记** ——
// 而那正是最需要被看见的失效方向。
func TestIngressSurfaceRegistryCoversEveryRoute(t *testing.T) {
	main := readSource(t, mainRouteFile)
	routes, unclassified := parseRoutes(t, main)

	if len(unclassified) > 0 {
		t.Fatalf("main.go 里有 %d 处 /v1 字面量既不是路由注册行、也不是日志串/客户端基址：\n  %s\n"+
			"这通常意味着出现了一种本门还不认识的路由注册写法 ⇒ 门会静默漏掉它。\n"+
			"请把它归类：要么扩展 parseRoutes 的注册行识别，要么在 isNonRouteLine 里说明它为什么不是路由",
			len(unclassified), strings.Join(unclassified, "\n  "))
	}

	// 下限的实测取值：218 号在当前 HEAD 上量到 **21 条唯一 /v1 路由**
	// （注册行共 25 行，多出的 4 行是 main.go:6419-6425 的 v2 二次注册）。
	// ⚠️ 第一版这里写的是 25 —— 那是**出现次数**不是**唯一路由数**，
	//   于是判据在自己建起来当天就红。取下限必须先分清「量的是什么」，
	//   并且按逐文件实测取值，不随手写整数（playbook §210）。
	// 21 是**紧**的：掉一条唯一路由就红；新增一条则由下面的「未登记」分支接手。
	const floorRoutes = 21
	occurrences := 0
	for _, ls := range routes {
		occurrences += len(ls)
	}
	t.Logf("main.go 解析出 %d 条唯一 /v1 路由（注册行 %d 行，下限 %d）",
		len(routes), occurrences, floorRoutes)
	if len(routes) < floorRoutes {
		t.Fatalf("只从 %s 解析出 %d 条唯一 /v1 路由，低于下限 %d —— 解析器很可能退化了，"+
			"此时「全部已登记」是空断言", mainRouteFile, len(routes), floorRoutes)
	}

	registered := map[string]string{}
	for _, s := range registry {
		name := strings.Join(s.Routes, ",")
		switch s.Plane {
		case planeInference, planeControl:
		default:
			t.Errorf("%s：Plane 必须是 planeInference 或 planeControl，拿到 %q", name, s.Plane)
		}
		for _, r := range s.Routes {
			if prev, dup := registered[r]; dup {
				t.Fatalf("登记表里 %s 出现两次（%s 与 %s）：同一路由不能登两次，"+
					"否则它的声明会被重复计数、把真实缺口藏起来", r, prev, name)
			}
			registered[r] = name
		}
	}

	// 一个源文件只登记一次。
	//
	// ⚠️ 这条是 218 号负控 NC3 逼出来的：把 /v1/embeddings 的 File 从
	// embeddings.go 改成 handler.go（后者同样恰好有 1 次 CheckBudget 与
	// 1 次 checkGatewayRateLimit）⇒ **在加这条断言之前，门照样全绿**。
	// 根因是「File 指向哪个文件」完全靠人写，而锚点计数对「另一个也合规的文件」
	// 一样成立 ⇒ 登记表与真实接线之间没有交叉验证。
	// HandlerVars 核对解决了一半（路由→handler 变量），File→handler 变量的映射
	// 仍是人工的；这条唯一性断言把「借用别人的文件蒙混过关」这条路堵上。
	ownerOfFile := map[string]string{}
	for _, s := range registry {
		name := strings.Join(s.Routes, ",")
		if prev, dup := ownerOfFile[s.File]; dup {
			t.Errorf("%s 与 %s 都登记了同一个 File %s —— "+
				"一个源文件只能登记一次：两条声明会互相掩盖，"+
				"且「把 File 指向另一个也合规的文件」就能骗过锚点计数",
				prev, name, s.File)
			continue
		}
		ownerOfFile[s.File] = name
	}

	var missing []string
	for path := range routes {
		if _, ok := registered[path]; !ok {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("新增的 /v1 数据面没有登记进 ingressguard.registry（%d 个未登记）：%v\n"+
			"请为它补一条 surface（并判定它的配额闸/限流是有、委派、还是没有）——"+
			"「忘了登记」正是这道门要拦的东西", len(missing), missing)
	}
}

// TestIngressSurfaceGateDeclarationsMatchCode 核对每条登记的声明与代码一致：
//  1. 路由在 main.go 里绑定的 handler 必须是该条目声明的 HandlerVars 之一
//     （防止登记表与 main.go 的接线脱钩）；
//  2. gateOwn ⇒ 锚点恰好 1 次；gateNone/gateDelegated ⇒ 恰好 0 次；
//  3. 每个声明的 HandlerVar 至少被一条路由用上（防死变量架空断言）。
//
// 取「恰好 N 次」而不是「存在/不存在」，是为了让任何一侧的真实变更都可见。
func TestIngressSurfaceGateDeclarationsMatchCode(t *testing.T) {
	main := readSource(t, mainRouteFile)
	routes, _ := parseRoutes(t, main)

	// 每个 handler 文件只读一次。
	cache := map[string]string{}
	src := func(t *testing.T, rel string) string {
		t.Helper()
		if s, ok := cache[rel]; ok {
			return s
		}
		s := readSource(t, rel)
		cache[rel] = s
		return s
	}

	check := func(s surface, name, anchor string, kind gateKind, why, delegate string) {
		t.Helper()
		got := countOf(src(t, s.File), anchor)
		switch kind {
		case gateOwn:
			if got != 1 {
				t.Errorf("%s（%s）：登记为 own，但 %q 出现 %d 次，期望恰好 1 次。"+
					"要么它不是 own，要么登记表过期了", name, s.File, anchor, got)
			}
		case gateNone, gateDelegated:
			if got != 0 {
				t.Errorf("%s（%s）：登记为 %s（理由：%s / 委派：%s），但 %q 出现了 %d 次。"+
					"代码已经变了 —— 若这是有意补上的闸，请把该行改成 gateOwn 并删掉理由",
					name, s.File, kind, why, delegate, anchor, got)
			}
			if kind == gateNone && why == "" {
				t.Errorf("%s：登记为 gateNone 却没有写理由 —— 「没有」必须说明为什么可接受，"+
					"否则下一个接手的人无法区分「有意」与「遗漏」", name)
			}
			if kind == gateDelegated && delegate == "" {
				t.Errorf("%s：登记为 gateDelegated 却没有填 Delegate —— 必须指明委派给谁，"+
					"否则「它自己没有闸」会被读成「它没有闸」（215/217 号已各踩过一次）", name)
			}
		default:
			t.Errorf("%s：未知的 gateKind %q", name, kind)
		}
	}

	usedVar := map[string]bool{}

	for _, s := range registry {
		name := strings.Join(s.Routes, ",")

		if s.File == "" {
			t.Errorf("%s：必须填 File —— 判据要核对锚点在哪个文件里，"+
				"控制面也不例外（否则「它不调上游」只是断言，没有可核对的证据）", name)
			continue
		}
		// ⚠️ 先读一次：控制面虽然不断言闸，但**文件必须真的存在且可读**，
		// 否则登记表里可以写一个不存在的路径而门照样绿。
		// （第一版这里写成「控制面 continue 掉」，而注释却写「仍要求文件存在」
		//   —— 注释与代码不符，正是本仓反复出现的那一类。）
		_ = src(t, s.File)

		// ── 接线核对：这条路由在 main.go 上到底交给了谁 ──────────────
		if len(s.HandlerVars) == 0 {
			t.Errorf("%s：必须填 HandlerVars —— 不核对 main.go 的接线，"+
				"「这个面有闸」就只是登记表自己的一句话", name)
			continue
		}
		for _, r := range s.Routes {
			lines, ok := routes[r]
			if !ok {
				// 覆盖面由上一个测试负责；这里不重复报错。
				continue
			}
			for _, ln := range lines {
				hit := ""
				for _, v := range s.HandlerVars {
					if strings.Contains(ln.Handler, v) {
						hit = v
						break
					}
				}
				if hit == "" {
					t.Errorf("%s：main.go:%d 把 %s 注册给了 %q，但登记表声明的 handler 是 %v —— "+
						"要么改登记表，要么改接线（把 File/闸的声明搬到现在真正被调用的那个 handler 上）",
						name, ln.LineNo, r, ln.Handler, s.HandlerVars)
					continue
				}
				usedVar[name+"|"+hit] = true
			}
		}

		// 控制面只要求「已登记 + 文件可读 + 接线对上」，不要求对闸表态。
		if s.Plane == planeControl {
			continue
		}
		check(s, name, budgetAnchor, s.Budget, s.BudgetWhy, s.BudgetDelegate)
		// 限流委派方与配额闸同一个（都走 chatHandler），故复用 Delegate 但用独立理由位。
		check(s, name, rateAnchor, s.RateLimit, s.RateLimitWhy, s.BudgetDelegate)
	}

	// 防死变量：声明了却没有任何路由用到 ⇒ 断言可能被架空。
	for _, s := range registry {
		name := strings.Join(s.Routes, ",")
		for _, v := range s.HandlerVars {
			if !usedVar[name+"|"+v] {
				t.Errorf("%s：声明的 HandlerVars 里有 %q 从未出现在任何一条路由的注册行上 —— "+
					"它是死变量，会让接线断言变得比看上去松（要么修对，要么删掉）", name, v)
			}
		}
	}
}
