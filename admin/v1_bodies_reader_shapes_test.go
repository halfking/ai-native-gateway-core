//go:build !integration

package admin

// v1 bodies 读方的**形状**分类与逐点判定（审计 §9.231）。
//
// # 这道门补的是什么
//
// §9.230 补了 `db.SessionFamilyBodiesSourceSQL()`，并在 handoff 里写下
// 「补了 helper 之后才谈改 bodies 腿」。那句话**隐含了一个错误前提**：
// 仿佛 25 个 bodies 读方是同一次机械替换。实测不是。
//
// 逐个读过真实 SQL 之后，它们至少分四种形状，而那个 helper **只 fit 一种**：
//
//	A. `ON rb.request_id = rl.request_id` 单键、只用 request_body/
//	   response_body/outbound_body —— **可以直接换**。
//	B. `ON rb.request_id = rl.request_id AND rb.ts = rl.ts` —— **换不了**。
//	   helper 不投影 `ts`（这是有意的，见 db 侧注释），换过去是**解析期 42703**。
//	   响亮失败是好事，但「响亮」不等于「已登记」。
//	C. 两次**顺序**查询（hot 短超时 → view 长超时）—— **不该换**。
//	   换成 UNION 子查询会毁掉刻意的延迟分层，文件自己的注释写着
//	   "intentionally separate so the common recent-request path never invokes
//	   the UNION view"。这是性能设计的改动，不是 repoint。
//	D. 只读体量（`count(*)` / `pg_column_size`）—— 不读正文，量的是 v1 自身。
//
// # 为什么登记表 + 机器交叉核对，而不是一个正则分类器
//
// 我先写了个正则分类器，**它自己有盲区**，而且盲区正好落在最要紧的地方：
//
//	① 别名假设成 `rb` ⇒ `cmd/compression-bench/main.go` 的
//	   `JOIN request_logs_bodies b ON b.request_id = rl.request_id AND b.ts = rl.ts`
//	   被判成「单键 JOIN」——而它是 B 类里最典型的那个。
//	② 「两次顺序查询」只认 `WHERE request_id = $1` ⇒ 带 `rl.` 前缀的变体
//	   （`admin/body_resolver.go`）被漏掉。
//
// ⇒ 「ts 等值读方有 3 个」这个结论，是**读源码**读出来的，不是分类器算出来的。
// 拿一个有已知盲区的分类器当门，等于把「量具测不到」记成「对象不存在」——
// 本项目反复写下的那一条。所以这里改成：**决策由人写并写清机制，
// 机器只核对一件它能可靠核对的事**（用到的 bodies 列是否都在合同内、是否绑了别名），
// 且只对**正向声明**（"helper-compatible"）生效。
//
// # 「已切换」这一档为什么不做形状核对
//
// `admin/session_bodies_source.go` 的 v1 依赖不是字面量 JOIN，
// 而是开关的**默认那一臂**（§9.230）。它按 `scanV1BodiesReaders` 的
// 「间接读方并入总体」计入总体，但源码里没有可解析的 JOIN ⇒
// 强制它满足形状核对会把一道正确的登记判成错。
// ⇒ 该档跳过形状核对，但仍然要写清 Reason（`TestV1BodiesReadersAreDeclaredWell` 查）。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

// 判定档位。
const (
	// shapeHelperCompatible：单键 JOIN + 列全在合同内 ⇒ 可直接换 helper。
	shapeHelperCompatible = "helper-compatible"
	// shapeJoinsOnTs：`ON … request_id = … AND …ts = …` ⇒ 换不了（helper 不投影 ts）。
	shapeJoinsOnTs = "joins-on-ts"
	// shapeTwoStepHotThenView：两次顺序查询（延迟分层）⇒ 不该换。
	shapeTwoStepHotThenView = "two-step-hot-then-view"
	// shapeSizeNotContent：只读体量/计数，不读正文。
	shapeSizeNotContent = "size-not-content"
	// shapeMustStayOnV1：读 v1 就是这个文件的工作本身（迁移源 / 对拍器）。
	shapeMustStayOnV1 = "must-stay-on-v1"
	// shapeAlreadySwitched：§9.230 已建的开关，v1 依赖在默认臂里。
	shapeAlreadySwitched = "already-switched"
)

// bodyReaderShape 是**人写**的判定。字段全是编译器不要求填的 ⇒
// 「登记」与「填了一个空结构体」在退出码上不可区分，
// 所以 TestV1BodiesReaderShapesAreClassified 挡住「登记了但说不清」。
type bodyReaderShape struct {
	Shape  string
	Reason string
}

var v1BodiesReaderShapes = map[string]bodyReaderShape{
	"admin/session_bodies_source.go": {
		Shape: shapeAlreadySwitched,
		Reason: "§9.230 建的 bodies 读端开关。v1 依赖是 `sessionBodiesFromSQL()` 的" +
			"**默认那一臂**（`request_logs_bodies_with_current_month`），不是字面量 JOIN。" +
			"消费点 admin/session_export.go:227 / admin/session_compare.go:903。" +
			"退役含义：这一条**必须最后退役**，因为它是切换的入口；" +
			"它的默认臂反转之日就是 bodies 退役之日。",
	},

	// ── B 类：ts 等值，换不了 ────────────────────────────────────────────
	"admin/body_resolver.go": {
		Shape: shapeJoinsOnTs,
		Reason: "`ON rb.request_id = rl.request_id AND rb.ts = rl.ts`（:214/:233）。" +
			"helper 不投影 ts ⇒ 换过去解析期 42703（响亮，但换不了）。" +
			"⚠ 更深一层：这个 ts 等值条件**实测几乎恒假**" +
			"（admin/session_bodies_batch.go 文件头记着 99.85% 的 bodies 行 ts 不等），" +
			"所以这条腿今天大概率本来就取不到值。改它属于**修正匹配语义**，" +
			"要先定「到底按 request_id 匹配还是按 (request_id, ts) 匹配」，" +
			"不是 repoint。",
	},
	"admin/compression_sessions.go": {
		Shape: shapeJoinsOnTs,
		Reason: ":144 的 `LEFT JOIN request_logs_bodies_with_current_month rb2` 走 ts 等值" +
			"（:106 的 countSQL 同形态）。压缩率统计按 (request_id, ts) 配对是" +
			"**这个指标的语义本身**（压缩前后同一轮），不是随手写的 JOIN —— " +
			"改成单键会把「同一轮的压缩率」算成「任意一轮的压缩率」。" +
			"⇒ 换源之前必须先决定新口径，否则指标定义被悄悄改掉。",
	},
	"cmd/compression-bench/main.go": {
		Shape: shapeJoinsOnTs,
		Reason: ":237 `JOIN request_logs_bodies b ON b.request_id = rl.request_id AND b.ts = rl.ts`，" +
			"且读的是**基表** `request_logs_bodies`（不经 view）。压测工具，" +
			"量的是 v1 自身的压缩行为；换源后它量的就不再是「v1 的压缩率」。" +
			"⇒ 它是**基准工具**，retire v1 时应随 v1 一起退役或显式改口径。",
	},

	// ── C 类：两次顺序查询，不该换 ───────────────────────────────────────
	"admin/logs.go": {
		Shape: shapeTwoStepHotThenView,
		Reason: "`fetchRequestOutboundBody` / `fetchRequestBodies` 各自是**两次顺序查询**：" +
			"先 `FROM request_logs_bodies_hot`（3s 超时，命中即返回），" +
			"未命中再 `FROM request_logs_bodies_with_current_month`（20s）。" +
			"文件注释明写这个分层是刻意的（“stage 2: columnar 月分区（可能慢，给 20s ctx）”）。" +
			"⇒ 换成 helper 的 UNION 子查询会让**每条**请求都去扫分区父表，" +
			"把「近期走热表」变成「近期也扫月分区」。这是性能设计的改动，" +
			"必须自带前后延迟读数，不能混在 repoint 里做。",
	},
	"admin/unified_detail.go": {
		Shape: shapeTwoStepHotThenView,
		Reason: "`pgBodyReader.loadOutboundBody`（:265-300）同样是 hot → view 两次顺序查询，" +
			"注释写着 “Try hot table first, then fall back to partitioned bodies table”。" +
			"⚠ 它的错误语义与 admin/logs.go **相反**：`pgx.ErrNoRows` 会被" +
			"翻成 `requestdetail.ErrNotFound` 并上抛 404。" +
			"⇒ 换源后若 session_bodies 覆盖不到，这个 404 是**响亮**的（好），" +
			"但仍会改变「详情页能否打开」——属于可见行为变更，不能当机械替换。",
	},

	// ── D 类：只读体量 ───────────────────────────────────────────────────
	"admin/data_lifecycle.go": {
		Shape: shapeSizeNotContent,
		Reason: ":237 `COUNT(DISTINCT request_id)` 7 天增长趋势 + :255 的 bodies 腿。" +
			"它不读正文，量的是「v1 存了多少、压了多少」。" +
			"⇒ 退役 v1 后这条趋势应当**改指会话族**（否则 requests 线继续涨、compressed 线冻结，" +
			"两条线分叉且无错误信号），但它换的是**统计对象**不是 JOIN，" +
			"和内容读方不是同一件事。",
	},
	"admin/data_lifecycle_blobs.go": {
		Shape: shapeSizeNotContent,
		Reason: "唯一的 bodies 引用是 `COALESCE(pg_column_size(rb.request_body), 0)`，" +
			"即「存量正文占多少字节、清理能省多少」。不读正文内容。" +
			"⚠ 它是 §9.226 建的 `bodiesUnaffectedJustification` 具名豁免之一，结论成立。",
	},
	"cmd/scenario_driver/main.go": {
		Shape: shapeSizeNotContent,
		Reason: ":115/:149 `SELECT count(*) FROM request_logs_bodies_hot WHERE ts > NOW() - INTERVAL '1 hour'`。" +
			"这是**观察 v1 有没有流量**的探针（判 S4 是否生效），不是读内容。" +
			"⇒ retire v1 时它的正确终态是**跟着 v1 一起退役**；" +
			"留着它会在 v1 消失后恒返回 0，而「0」在这里**看起来像「v1 停了」**——" +
			"正是本项目记过的「冻结本身就是信号」那一档。",
	},

	// ── 工具：读 v1 就是它的工作本身 ─────────────────────────────────────
	"cmd/tools/backfill_session_bodies/main.go": {
		Shape: shapeMustStayOnV1,
		Reason: "★ 它就是**从 v1 读、往 session_bodies 填**的那个工具。" +
			"把它的 bodies 源换成 session_bodies 等于让它读自己写的表 → 自毁。" +
			"⇒ 退役顺序上它必须排在**回填跑完之后**，且 retire v1 时要一并退役" +
			"（回填完成 = 它的使命完成），不是迁往会话族。",
	},
	"cmd/tools/validate_sessions_v2/loader.go": {
		Shape: shapeMustStayOnV1,
		Reason: "★ 它的职责就是 **v1 ↔ session 对拍判镜像漂移**（`LoadV1Turns` 两步查 v1 母表 + bodies）。" +
			"它读 v1 **不是遗漏，是被测对象**。" +
			"⇒ retire v1 之后它要么退役、要么改成「对拍更老的一代」，" +
			"**不存在「迁到会话族」这个选项**——那会让它对着自己校验自己。" +
			"这是 §9.231 最要紧的一条结论：两个工具在 v1 退役后**无处可去**。",
	},

	// ── A 类：单键 JOIN + 列全在合同内 ────────────────────────────────────
	"admin/compression_stats.go": {
		Shape: shapeHelperCompatible,
		Reason: "3 处 `LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id`" +
			"（:55/:144/:265），用 `rb.request_body` / `rb.response_body` / `rb.outbound_body`，" +
			"**无 ts 条件**。⇒ 可直接换 `db.SessionFamilyBodiesSourceSQL()`。" +
			"⚠ 停写后果已登记为 silently_degraded_content（§9.35 分类表）：" +
			"换源后 total/strategy 分布仍有数而压缩率分项会塌 —— 换的时候要一起看分项。",
	},
	"admin/session_bodies_batch.go": {
		Shape: shapeJoinsOnTs,
		Reason: "★ **本轮被自己的门改判的一条。** 起初我按文件名把它归为可迁移 —— " +
			"「纯点查助手、`FROM … rb`、`WHERE rb.request_id IN (unnest($1))`」，" +
			"看起来是全表最 trivial 的一个。`TestV1BodiesReaderShapeDecisionsAreSupported` " +
			"报它**用到了 `rb.ts`**：`sessionBodiesByRequestIDAndTSSQL`（:82-92）投影了 " +
			"`rb.ts` 并用 `WHERE (rb.request_id, rb.ts) IN (SELECT * FROM unnest($1,$2) …)` 配对。" +
			"⇒ helper 不投影 ts ⇒ **整个文件不能换**（不是「一半能换」，因为换源要改函数体）。\n" +
			"⚠ 另注：那个元组变体实测几乎恒空（文件头：同一批键里 99.85% 的 ts 不相等）。" +
			"**若确认它已经无产出，直接退役它比迁移它更省事** —— 这条要先问清楚。\n" +
			"⚠ 影响面比文件本身大：`sessionBodiesByRequestIDSQL` 的消费点有 " +
			"admin/session_summary_v2.go 与 admin/session_compare.go，改它要连消费点一起数。",
	},
	"admin/session_title.go": {
		Shape: shapeHelperCompatible,
		Reason: "`:192` `LEFT JOIN request_logs_bodies_with_current_month rb`，无 ts 条件。" +
			"换源后 bodies 腿取不到时语料降级为 preview 兜底（该文件已有 40 rune 以下的 400 分支）。",
	},
	"admin/session_sanitize_matches.go": {
		Shape:  shapeHelperCompatible,
		Reason: "`:227` 单键 LEFT JOIN，无 ts。",
	},
	"admin/no_topic_session.go": {
		Shape:  shapeHelperCompatible,
		Reason: "`:146/:321` 两处单键 LEFT JOIN，无 ts。",
	},
	"admin/logs_summary.go": {
		Shape:  shapeHelperCompatible,
		Reason: "`:201/:263` 两处单键 LEFT JOIN，无 ts。",
	},
	"admin/quality_correlations.go": {
		Shape: shapeHelperCompatible,
		Reason: "`:229` `LEFT JOIN request_logs_bodies rb ON rb.request_id = rl.request_id`，无 ts。" +
			"⚠ 它读的是**基表** `request_logs_bodies`（不经 view），且 `WHERE rl.is_auto_request = TRUE` —— " +
			"**这 29,691 条探针流量正是 §9.229 判定「不进会话族、按设计」的那批**。" +
			"⇒ 换源会**改变这个指标的口径**（从「探针流量」变成「会话流量」）。" +
			"这不是空指针问题，是分子定义变了；必须显式决定。",
	},
	"admin/memora_handlers.go": {
		Shape: shapeHelperCompatible,
		Reason: "`:811` 单键 LEFT JOIN，无 ts。⚠ 该文件在停写分类表里记的是 " +
			"degraded_content，且历史上因「同一文件取更危险档」被改判过一次 —— 改它前先读那条 Note。",
	},
	"admin/auto_title_generator.go": {
		Shape:  shapeHelperCompatible,
		Reason: "`:840` 单键 LEFT JOIN，无 ts。",
	},
	"domains/sessionforensics/export.go": {
		Shape:  shapeHelperCompatible,
		Reason: "`:52/:72` 两处单键 LEFT JOIN，无 ts。取证导出，改源后正文缺失会体现在导出包里。",
	},
	"domains/hooks/goal/history_store.go": {
		Shape:  shapeHelperCompatible,
		Reason: "`:85` `LEFT JOIN request_logs_bodies rb`（基表），无 ts。",
	},
	"bg/passive_probe_listener.go": {
		Shape: shapeHelperCompatible,
		Reason: "`:182` 单键 LEFT JOIN，无 ts。探针监听器 —— 同样落在 " +
			"`is_auto_request` 那批流量上，口径问题同 quality_correlations。",
	},
	"domains/sessionsummary/summarizer.go": {
		Shape: shapeHelperCompatible,
		Reason: "`:651/:694` 两处单键 LEFT JOIN，无 ts。⚠ **它的会话族替身已经存在**：" +
			"`SetMessageSource(v2SessionBodiesSource)`（停写分类表 §9.35 记着）。" +
			"⇒ 这一条不是「能不能换」，是「**为什么默认还是 pgRequestLogsSource**」——" +
			"summarizer.go:127 `NewSummarizer` 直接 `&pgRequestLogsSource{}`。" +
			"换源 = 改那个默认构造，是一行的事，但会改**所有**会话摘要的语料来源。",
	},
	"domains/sessionsummary/system_prompt_prefix.go": {
		Shape: shapeHelperCompatible,
		Reason: "`:177` `JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id`" +
			"（INNER JOIN，无 ts），取 `rb.request_body::text`。" +
			"⚠ 它是**同族**文件：注释写着「先落 request_logs_bodies_hot，直接读父表会错过」——" +
			"⇒ 它刻意读 view（hot ∪ 父表）而不是 hot。helper 同样是 hot ∪ 父表，**形状对得上**。",
	},
}

// bodiesAliasRE 从 `JOIN request_logs_bodies… <别名> ON` 里**解析出别名**。
//
// ⚠ 别名不能假设成 `rb`：cmd/compression-bench/main.go 用的是 `b`。
// 我第一版的分类器就死在这里 —— 它把一个 ts 等值 JOIN 判成了单键 JOIN。
var (
	// bodiesJoinAliasRE：`JOIN request_logs_bodies… <别名> ON …`
	bodiesJoinAliasRE = regexp.MustCompile(
		`(?i)\bJOIN\s+(?:\w+\.)?request_logs_bodies\w*\s+(\w+)\s+ON\b`)
	// bodiesFromAliasRE：`FROM request_logs_bodies… <别名>`（**无** ON）。
	//
	// ⚠ 第一版只有 JOIN 形态，于是 `admin/session_bodies_batch.go` 的
	// `FROM request_logs_bodies_with_current_month rb` 解析不出别名 ——
	// 门按设计**拒绝了它的正向声明**（「解析不出 ≠ 兼容」），是对的。
	// 但让量具看不见一个真实读方比看见它更糟：看不见就永远不会被判。
	// ⇒ 补上 FROM 形态。**不是放宽判据**，是让解析器覆盖另一种同样无歧义的写法。
	//
	// 排除关键字是承重的：没有它，`FROM request_logs_bodies_hot WHERE …`
	// 会把 `WHERE` 当成别名，于是后面每个 `WHERE.<col>` 都算成列引用。
	bodiesFromAliasRE = regexp.MustCompile(
		`(?i)\bFROM\s+(?:\w+\.)?request_logs_bodies\w*\s+(\w+)\b`)
	sqlKeywordAlias = map[string]bool{
		"where": true, "on": true, "join": true, "left": true, "right": true,
		"inner": true, "outer": true, "full": true, "cross": true, "limit": true,
		"order": true, "group": true, "and": true, "or": true, "as": true, "select": true,
	}
)

// resolveBodiesAlias 解析文件里 bodies 关系的别名。
// JOIN 形态优先（它带 ON，语义最明确）。
func resolveBodiesAlias(src string) string {
	if loc := bodiesJoinAliasRE.FindStringSubmatchIndex(src); loc != nil {
		return src[loc[2]:loc[3]]
	}
	if loc := bodiesFromAliasRE.FindStringSubmatchIndex(src); loc != nil {
		if a := src[loc[2]:loc[3]]; !sqlKeywordAlias[strings.ToLower(a)] {
			return a
		}
	}
	return ""
}

// bodiesAliasColRE 匹配 `<别名>.<列>`，别名由上面解析出来。
func bodiesAliasColRE(alias string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(alias) + `\.(\w+)`)
}

// measuredBodiesShape 是机器**能可靠核对**的那几项。
type measuredBodiesShape struct {
	alias          string
	joinsOnTs      bool
	usesTs         bool            // 任何 <alias>.ts 引用（含 WHERE 元组），不只是 ON 等值
	columns        map[string]bool // 用到的 bodies 列
	outsideOf      []string        // 合同之外的列
	twoStepHotView bool
	sizeOnly       bool
	readsContent   bool
}

// measureBodiesShape 解析一个文件的 bodies 读法。
//
// 返回 ok=false 表示**解析不出别名**（例如该文件的 v1 依赖在开关默认臂里）。
// 「解析不出」与「解析出来且形状是 A」是**两件事**，必须分开 ——
// 把前者报成后者就是 §9.45「把不知道报成安全」。
func measureBodiesShape(t *testing.T, root, rel string) (m measuredBodiesShape, ok bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	src := string(raw)

	m.alias = resolveBodiesAlias(src)
	if m.alias == "" {
		return measuredBodiesShape{}, false
	}
	m.columns = map[string]bool{}
	for _, mm := range bodiesAliasColRE(m.alias).FindAllStringSubmatch(src, -1) {
		m.columns[strings.ToLower(mm[1])] = true
	}
	// ⚠ 判据是「**用到 ts**」，不是「ts 等值 JOIN」。
	//
	// 第一版只测 `ON rb.ts = rl.ts`，于是 `admin/session_bodies_batch.go` 的
	// `WHERE (rb.request_id, rb.ts) IN (SELECT * FROM unnest($1,$2) …)`
	// —— 它在 WHERE 元组里用 ts，根本不是 JOIN —— 被判成「不用 ts，可换」。
	// 而它恰恰是全表最像「trivial 可迁移」的那个文件（纯点查助手）。
	// ⇒ 「能不能换」的真实问题是「用到的列在不在合同内」，ts 出现在
	// 任何位置都不在合同内。ON 形态只作为附加信息保留。
	m.usesTs = m.columns["ts"]
	m.joinsOnTs = regexp.MustCompile(
		`(?i)\b` + regexp.QuoteMeta(m.alias) + `\.ts\s*=\s*\w+\.ts`).MatchString(src)

	// 合同之外用到的列。
	contract := map[string]bool{}
	for _, c := range dbpkg.SessionFamilyBodiesSourceColumns {
		contract[c] = true
	}
	for c := range m.columns {
		if !contract[c] {
			m.outsideOf = append(m.outsideOf, c)
		}
	}
	sort.Strings(m.outsideOf)

	// 两次顺序查询：同一文件里同时出现 hot 与 view 两个 bodies 源，各自带 WHERE。
	hotLeg := regexp.MustCompile(`(?i)FROM\s+request_logs_bodies_hot\b`).MatchString(src)
	viewLeg := regexp.MustCompile(`(?i)FROM\s+request_logs_bodies_with_current_month\b`).MatchString(src)
	m.twoStepHotView = hotLeg && viewLeg

	m.sizeOnly = strings.Contains(src, "pg_column_size") ||
		regexp.MustCompile(`(?i)count\(\*\)\s+FROM\s+request_logs_bodies`).MatchString(src) ||
		regexp.MustCompile(`(?i)COUNT\(DISTINCT\s+request_id\)`).MatchString(src)
	m.readsContent = false
	for _, c := range []string{"request_body", "response_body", "outbound_body"} {
		if m.columns[c] {
			m.readsContent = true
		}
	}
	if strings.Contains(src, "pg_column_size") {
		m.readsContent = true
	}
	return m, true
}

// TestV1BodiesReaderShapesAreClassified 双向门：实测总体 == 登记表。
//
//   - 实测到但没登记 ⇒ 红（「未逐点判定」）
//   - 登记了但实测不到 ⇒ 红（读法变了，登记该删）
//
// 总体沿用 scanV1BodiesReaders —— **同一个 SSOT**，不另抄一份名单。
func TestV1BodiesReaderShapesAreClassified(t *testing.T) {
	root := repoRootFromCaller(t)
	measured := scanV1BodiesReaders(t, root)

	var unregistered, stale []string
	for f := range measured {
		if _, ok := v1BodiesReaderShapes[f]; !ok {
			unregistered = append(unregistered, f)
		}
	}
	for f := range v1BodiesReaderShapes {
		if _, ok := measured[f]; !ok {
			stale = append(stale, f)
		}
	}
	sort.Strings(unregistered)
	sort.Strings(stale)
	t.Logf("v1 bodies 读方：实测 %d 文件 / 判定 %d 文件", len(measured), len(v1BodiesReaderShapes))

	if len(measured) == 0 {
		t.Fatal("v1 bodies 读方实测为 0 —— 这是量具坏了，不是「bodies 腿没人读了」。" +
			"在此状态下下面那道门会全绿，而一个依赖都没被判定。")
	}
	if len(unregistered) > 0 {
		t.Errorf("这些 bodies 读方还没有形状判定：%v\n"+
			"  先读它的真实 SQL（别靠正则猜：别名未必是 rb、JOIN 未必是单键），"+
			"再在 v1BodiesReaderShapes 里写 Shape + Reason。", unregistered)
	}
	if len(stale) > 0 {
		t.Errorf("这些形状判定已失效（实测总体里没有该文件）——多半是读法变了或已 repoint：%v\n"+
			"  ⚠ 别直接删条目：先确认它是真的不再读 v1，还是**又变成了间接读法**"+
			"（那样应该进 indirectRequestLogsReaders，见 §9.230）。", stale)
	}
}

// TestV1BodiesReaderShapesAreDeclaredWell 挡住「登记了但说不清」。
//
// ⚠ 没有这道门，填 25 个空结构体就能让上面那道门全绿 —— 25 个 `bodyReaderShape{}`
// 是合法的 Go，编译器一个错都不报。而「这些读方各自该怎么处置」正是本轮唯一的产出。
//
// 复用隔壁 `TestV1BodiesReadersAreDeclaredWell` 的理由：那张表用 `indirectReader`、
// 这张表用 `bodyReaderShape`，结构体不同所以那道门**不覆盖这里** ——
// 这正是同项目里已经踩过一次的那个坑（§9.226：两张表用同一个结构体，
// 于是必须各自再写一道；这里反向——结构体不同，同样必须各自写）。
func TestV1BodiesReaderShapesAreDeclaredWell(t *testing.T) {
	valid := map[string]bool{
		shapeHelperCompatible: true, shapeJoinsOnTs: true,
		shapeTwoStepHotThenView: true, shapeSizeNotContent: true,
		shapeMustStayOnV1: true, shapeAlreadySwitched: true,
	}
	for file, d := range v1BodiesReaderShapes {
		if !valid[d.Shape] {
			t.Errorf("%s：Shape = %q，不是已声明的档位。可选：%s\n"+
				"  ⚠ 新增档位必须同时在下面两处登记，否则「不可换」会被默认分支吞掉：\n"+
				"    ① 本函数的 valid 集合 ② TestV1BodiesReaderShapeDecisionsAreSupported 的日志列表",
				file, d.Shape, strings.Join(sortedKeys(valid), " / "))
		}
		if strings.TrimSpace(d.Reason) == "" {
			t.Errorf("%s：Reason 为空——空理由的登记等于没有登记。\n"+
				"  这一项要回答「退役后由什么替代、为什么那个替代够用」"+
				"（或「为什么这个读方本来就该读 v1」）。", file)
		}
	}
	if len(v1BodiesReaderShapes) == 0 {
		t.Fatal("判定表为空 —— 上面两道门会因「实测为 0」而 Fatal 或全绿，" +
			"而一个读方都没被判定。")
	}
}

// TestV1BodiesReaderShapeDecisionsAreSupported 交叉核对「人写的判定」与「机器读到的形状」。
//
// ★ 门是**单向不对称**的：只对 `helper-compatible`（正向声明）做核对。
//
//	声明可换、实测却绑了 ts 或用了合同外的列 ⇒ **红**。
//	这是本表唯一会咬人的方向：它拦的是「照着名字替换」——
//	本轮我自己就差点这么干（body_resolver 的 ts 等值 JOIN）。
//
// 反方向（声明不可换、实测像可换）**不判红**：那可能只是需要人判断
// （口径变化、性能设计），把它们判红等于逼人谎报以过门。
func TestV1BodiesReaderShapeDecisionsAreSupported(t *testing.T) {
	root := repoRootFromCaller(t)

	byShape := map[string][]string{}
	for file, d := range v1BodiesReaderShapes {
		byShape[d.Shape] = append(byShape[d.Shape], file)
	}
	for k := range byShape {
		sort.Strings(byShape[k])
	}
	for _, s := range []string{shapeHelperCompatible, shapeJoinsOnTs, shapeTwoStepHotThenView,
		shapeSizeNotContent, shapeMustStayOnV1, shapeAlreadySwitched} {
		t.Logf("%-24s %d 个 %v", s, len(byShape[s]), byShape[s])
	}

	for _, file := range byShape[shapeHelperCompatible] {
		m, ok := measureBodiesShape(t, root, file)
		if !ok {
			t.Errorf("⚠ %s 声明 helper-compatible，但**解析不出 bodies 别名** —— "+
				"要么它已经改读法了，要么别名形态超出本门的正则。"+
				"「解析不出」不等于「兼容」，别让它白拿一个正向声明。", file)
			continue
		}
		if m.usesTs {
			t.Errorf("⚠ %s 声明 helper-compatible，但实测**用到了 bodies 的 ts 列**（别名 %q，ON 等值=%v）。\n"+
				"  db.SessionFamilyBodiesSourceSQL() **不投影 ts**（这是有意的，见 db 侧注释）——\n"+
				"  照名字替换的结果是解析期 42703。这条应改判为 %s。",
				file, m.alias, m.joinsOnTs, shapeJoinsOnTs)
		}
		if len(m.outsideOf) > 0 {
			t.Errorf("⚠ %s 声明 helper-compatible，但用到了合同之外的 bodies 列 %v（别名 %q）。\n"+
				"  helper 只暴露 %v。缺列是解析期 42703；多投影则是无谓的列存储。",
				file, m.outsideOf, m.alias, dbpkg.SessionFamilyBodiesSourceColumns)
		}
		t.Logf("helper-compatible %s：别名=%q 列=%v 用到ts=%v 两次顺序=%v",
			file, m.alias, keysOf(m.columns), m.usesTs, m.twoStepHotView)
	}
}

// TestV1BodiesHelperContractHasNoTs 钉住「helper 不投影 ts」这个**决定**。
//
// 它是本轮三处「不能换」判定的技术根据。若将来有人为了「让更多读方能换」
// 而加上 ts，这道门会红并要求显式改判 —— 因为 ts 在两侧的相等率极低
// （实测 99.85% 不等），加上它会把一个响亮的解析期错误
// 换成一个静默的行数变化。
func TestV1BodiesHelperContractHasNoTs(t *testing.T) {
	for _, c := range dbpkg.SessionFamilyBodiesSourceColumns {
		if c == "ts" {
			t.Fatal("SessionFamilyBodiesSourceColumns 里出现了 ts —— " +
				"两侧 bodies 行的 ts 实测 99.85% 不相等，加上它会让 ts 等值 JOIN " +
				"从「解析期 42703」变成「静默少行」。要加的话先改本判据并写清代价。")
		}
	}
	if len(dbpkg.SessionFamilyBodiesSourceColumns) == 0 {
		t.Fatal("列集合为空 —— 门会认为「任何列都在合同内」，于是全部读方都合法。")
	}
}

func keysOf(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
