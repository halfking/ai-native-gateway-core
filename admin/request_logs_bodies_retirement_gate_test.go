//go:build !integration

package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// v1 族里 **bodies 腿**的退役门（审计 §9.226）。
//
// # 这道门补的是一个「量具看不见，所以报 clean」的口子
//
// `TestRequestLogsRetirementExposure` 把文件分成 breaks / undercounts / clean，
// 判据是**该文件的 v1 SQL 字面量里出现了哪些 canonical 合同列**。
// `admin/session_export.go` 因此落在 **clean**：
//
//	FROM ` + dbpkg.SessionFamilyTurnsForSessionSQL() + ` rl
//	LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id
//	… COALESCE(rb.request_body, '{}'::jsonb) AS request_body …
//
// ⚠ **上面这两行是 §9.226 当时的源码。§9.230 之后不是了**：bodies 腿改成经
// `db.SessionBodiesSourceSQL()` 取源 ⇒ 该文件源码里**一个 bodies 关系名字面量都没有**。
// ⇒ 引用这一段当现状会误导下一个人，必须连着看 scanV1BodiesReaders 末尾
// 那段「间接读方并入总体」的注释（本轮新增，否则总体会静悄悄少两个人）。
//
// 但它读的 `request_body` / `response_body` **不在 canonical 合同里**——
// 合同是 `request_logs` 自己的 118 列，而 bodies 是**另一张表**。
// ⇒ 「没有命中暴露列」在这里的含义是**本量具不测 bodies**，
// 不是「这个文件退役后照常工作」。
//
// 这个区分是承重的，因为它指向的失效形态是本文件族反复写下的那句：
// **一个返回 18% 数据的查询，和一个正常工作的查询，从外面看一模一样。**
// bodies 这条更糟一档：上面那句 SQL 写的是 `COALESCE(rb.request_body, '{}')`，
// 所以 DROP 之后它**不报错、不返回空、照常导出一个完整的会话包**——
// 只是每条消息的正文都是 `{}`。用户拿到的是「看起来成功的空数据」。
//
// # 为什么不能用「clean 就是安全」
//
// 事故形态与 §9.167 同型：那里的三个文件因为别名正则丢了一个限定符，
// 拿到空列集合后被判成 `repoint-safe`；这里是 bodies 列不在合同里，
// 于是被判成 `clean`。两者的错误方向一致：**没有证据被当成了通过。**
// §9.167 的处置是「零列即错误，不是日志」；这里照抄那条处置。
//
// # 口径
//
// 与 requestLogsReadInventory 同源（同一批非 _test.go 生产文件、同样的
// 注释剔除），只把关系名收窄到 bodies 腿。关系名**从 SSOT 推导**而不是手抄：
// v1BaseTableNames + viewChainNames 里凡是名字含 `bodies` 的都算
// （本项目栽过同族错误：再抄第三份名单，两份工具迟早分叉）。

// v1BodiesRelations 返回 v1 族里 bodies 腿的全部关系名。
func v1BodiesRelations(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, n := range append(append([]string{}, v1BaseTableNames...), viewChainNames(t)...) {
		l := strings.ToLower(n)
		if l == "" || !strings.Contains(l, "bodies") {
			continue
		}
		out = append(out, l)
	}
	sort.Strings(out)
	// 地板断言：bodies 腿被推导空了 ⇒ 上游 SSOT 坏了，不是「族里就这么点」。
	// 期望的 4 个是 request_logs_bodies / _hot / _with_current_month /
	// _hot 的包装视图；少了任何一个都会让这道门变成恒零。
	if len(out) < 3 {
		t.Fatalf("v1 bodies 关系名只推导出 %d 个（期望 ≥3）：%v —— "+
			"这不是「bodies 腿就这么点」，是上游 SSOT 推导坏了", len(out), out)
	}
	return out
}

// v1BodiesReadPattern 由 SSOT 构出，**最长在前**（Go 的 alternation 是
// leftmost-first，不是 longest-match；写成 (request_logs_bodies|request_logs_bodies_hot)
// 会先匹配前缀，别名整支被丢掉——v1AliasRe 的注释记过同族事故）。
func v1BodiesReadPattern(t *testing.T) *regexp.Regexp {
	t.Helper()
	names := v1BodiesRelations(t)
	alt := make([]string, len(names))
	for i, n := range names {
		alt[i] = regexp.QuoteMeta(n)
	}
	sort.Slice(alt, func(i, j int) bool { return len(alt[i]) > len(alt[j]) })
	return regexp.MustCompile(`(?i)\b(?:from|join)\s+(` + strings.Join(alt, "|") + `)\b`)
}

// scanV1BodiesReaders 数每个生产文件里 bodies 腿的读点数。
func scanV1BodiesReaders(t *testing.T, root string) map[string]int {
	t.Helper()
	pat := v1BodiesReadPattern(t)
	out := map[string]int{}
	skipDir := map[string]bool{".git": true, "docs": true, "node_modules": true, "vendor": true}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n := 0
		for _, line := range strings.Split(string(src), "\n") {
			if !pat.MatchString(line) {
				continue
			}
			trimmed := strings.TrimSpace(line)
			// 注释不是读点（与 requestLogsReadInventory 同一处置）。
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "/*") {
				continue
			}
			n++
		}
		if n > 0 {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			out[filepath.ToSlash(rel)] = n
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	// 间接 bodies 读方并入总体（审计 §9.230）。
	//
	// ⚠ **不加这一段就是一次假绿的前奏。** `admin/session_bodies_source.go`
	// 的 v1 那一臂是一行 `return "request_logs_bodies_with_current_month rb"` ——
	// 它**没有** FROM/JOIN 关键字，所以按行扫描测到 0 处、整个文件不进总体。
	// 而它的两个消费点（session_export.go / session_compare.go）在改成经这个
	// 开关取 bodies 源之后，源码里也不再有任何 bodies 关系名字面量。
	// ⇒ 三处一起从总体里消失，总体从 26 文件/44 调用点缩到 24/42，
	// 而**一个 bodies 依赖都没被评估过**。这是本文件头注释警告的那种
	// 「扫描器扫不到 ⇒ 门全绿」的前半段，只是这次门仍然红，只是红得**少了两个人**。
	//
	// 处置与 §9.49 的两张表分工一致：字面量读方按行扫，间接读方由
	// indirectRequestLogsReaders 登记并在这里并入。判据是 `ResolvesTo`
	// 落在 v1 bodies 关系名集合里 —— 不是「文件名叫 session_bodies 就收」。
	for file, e := range indirectRequestLogsReaders {
		rel := strings.ToLower(e.ResolvesTo)
		if !bodiesRelIsV1Bodies(t, rel) {
			continue // 读的是 turns 族，与本门总体无关
		}
		if out[file] == 0 {
			out[file] = 1
		}
		// ★ 消费点也要并进来（§9.232）。它们把 bodies 腿改走切换层后，
		// 源码里不再有 bodies 关系名字面量 ⇒ 会**逐个**从本门总体里消失。
		// 而本门**本来就故意红** ⇒ 少 11 个还是少 15 个都不改退出码。
		// 这一段是 §9.230.3 那个坑的批量版本。
		if e.SwitchFunc != "" {
			for c := range indirectSourceConsumers(t, root) {
				if out[c] == 0 && c != file {
					out[c] = 1
				}
			}
		}
	}
	return out
}

// bodiesRelIsV1Bodies 判断一个登记的 ResolvesTo 是否落在 v1 bodies 关系名集合里。
func bodiesRelIsV1Bodies(t *testing.T, rel string) bool {
	t.Helper()
	for _, n := range v1BodiesRelations(t) {
		if rel == n || rel+"_with_current_month" == n || rel+"_hot" == n {
			return true
		}
	}
	return false
}

// v1BodiesReaders 是**已复核**的 bodies 读方登记。
//
// 登记一项要写清三件事，缺一不可（沿用 indirectReader 的形状）：
// 读哪张 bodies 关系、退役后由什么替代、为什么那个替代够用。
// 只写文件名不写替代来源的登记，等于把「已看过」和「没问题」混成一句话。
//
// # 2026-10-13 停写复审计轮：25 文件逐点评估完毕，本表首次填齐
//
// 评估依据（真库实测，本地 host-first 库，翻转点 2026-10-13 07:01:04）：
//
//	request_logs / request_logs_bodies        35,506 行（冻结）
//	session_turns / session_bodies            12,011 行（翻转后持续承接）
//	v1_only（bodies 只在 v1）                 23,795 行 = 探针观测 22,951
//	                                          （无会话头，R51 设计不镜像）
//	                                          + 回环/非终态 844
//	带会话头而未镜像（drift 校验器口径的真损失候选）= 0
//
// ⇒ 会话族对「应镜像流量」的正文覆盖是完整的；DROP request_logs_bodies
// 不丢任何用户可见正文。会丢的只有两类已登记可接受损失：探针观测正文
// （其事实主存 node_probe_runs 族）与 title/summary 回环正文（生成器中间
// 产物，非用户轮次）。生产删表前由 owner 在生产库复核同一口径计数。
//
// 详见 docs/audit/2026-10-13-s3-wave4-bodies-retirement-assessment.md。
//
// bodiesSwitchRetireNote 是「经切换层取 bodies」读方的共同退役理由；
// 抽成常量避免十几份手抄漂移（判据纪律：理由与判据同源）。
const bodiesSwitchRetireNote = "退役后替代：切换层 v1 臂删除，SessionFamilyBodiesSourceSQL（session_bodies hot∪父表，request_id/request_body/response_body/outbound_body 四列合同）转唯一来源。" +
	"为什么够用：取源已走 db.SessionBodiesSourceSQL()（storage.session_bodies_native_read=true ⇒ 会话臂，非「改指切换层但停在 v1 臂」的假迁移）；" +
	"2026-10-13 停写复审计实测带会话头未镜像行=844 全为回环/非终态（真损失 0），会话臂覆盖全部用户可见正文。"

var v1BodiesReaders = map[string]indirectReader{
	// ── 组 1：切换层消费点（间接读方，已开会话臂）────────────────────────
	"admin/session_title.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason:     "loadTaskLogsForTitle 手动会话标题语料。" + bodiesSwitchRetireNote,
	},
	"admin/memora_handlers.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason:     "handleSessionMessages（/api/sessions/:id/messages 查看器）。" + bodiesSwitchRetireNote,
	},
	"admin/logs_summary.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason:     "loadSessionLogsForSummary / loadSessionLogsBySessionID 摘要语料两站点。" + bodiesSwitchRetireNote,
	},
	"admin/no_topic_session.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason:     "无主题会话消息查看器 + 标题语料两站点。" + bodiesSwitchRetireNote,
	},
	"admin/auto_title_generator.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason:     "AutoTitleGenerator.loadSessionLogsForTitle 自动标题语料。" + bodiesSwitchRetireNote,
	},
	"admin/session_sanitize_matches.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason:     "loadOutboundBodySnippet 敏感命中片段预览（outbound_body，fallback request_body）。" + bodiesSwitchRetireNote,
	},
	"admin/compression_stats.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason:     "handleCompressionStats 三站点（估 token / 策略聚合 / 小时时序，outbound_body+request_body）。" + bodiesSwitchRetireNote,
	},
	"admin/session_export.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason:     "sessionExportMessagesSQL 会话导出包消息（request_body/response_body）。" + bodiesSwitchRetireNote,
	},
	"admin/session_compare.go": {
		Family:     familyBodies,
		ResolvesTo: "request_logs_bodies",
		Reason: "generateHandoffSummary（handoff 交接摘要）经切换层。" + bodiesSwitchRetireNote +
			"对比主路径的批量正文（querySessionBodiesByRequestID）已于 2026-10-13 固定读会话族" +
			"（消费方 turns 腿本就是会话族；停写后新会话正文只在 session_bodies），解析器已补" +
			"request_delta/response_delta 数组形状臂（parseMessagesFromBody/parseResponseMessages）。",
	},
	"domains/sessionsummary/summarizer.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason:     "getSessionMessagesQuery / GetMessagesSince 摘要 V1 消息源两站点。" + bodiesSwitchRetireNote,
	},
	"domains/sessionsummary/system_prompt_prefix.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason:     "GetSystemPromptPrefix 摘要 system prompt 前缀（首轮请求体）。" + bodiesSwitchRetireNote,
	},
	"domains/sessionforensics/export.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason:     "取证会话包导出两份 SQL（ExportFromTx / ExportSession，双字面量由 TestForensicsExportSQLVariantsStayInSync 钉同步）。" + bodiesSwitchRetireNote,
	},
	"bg/passive_probe_listener.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason: "pollNewErrors 失败行 200 字符预览（response_body）。注意它读的是**业务失败行**正文——" +
			"查询带 ProbeTrafficExclusionPredicateView 显式排除探针，不是探针观测读方；终态失败行" +
			"按钩子语义会被镜像（response_delta 非空）。" + bodiesSwitchRetireNote +
			"整只监听器的驱动读（v1 turns 面）冻结是另一张表的登记项，不属本门。",
	},

	// ── 组 2：字面 v1 腿 + 会话兜底腿并存的点查族（三阶段读取）──────────
	"admin/logs.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason: "getLog 两阶段 body 读取：v1 腿（fetchRequestOutboundBody/fetchRequestBodies 的 hot+view" +
			"四处字面站点，request_id 单键）服务冻结历史；阶段3 已有会话族兜底（fetchSessionOutboundBody/" +
			"fetchRequestBodies 尾腿经 SessionFamilyBodiesSourceSQL）。退役后替代：删 v1 两腿，会话腿转唯一" +
			"（会话臂已覆盖全部镜像行，见 bodiesSwitchRetireNote 的覆盖实测；探针/回环请求_id 的点查返回" +
			"空正文属已登记可接受损失）。",
	},
	"admin/body_resolver.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason: "lookupControlledBody 受控正文点查：阶段1/2 为 v1 字面腿（hot+view，request_id+ts 等值——" +
			"v1 家族内合法的消歧键），阶段3 会话族兜底（request_id 单键，S4 交接加）。退役后替代：删 v1 两腿，" +
			"会话腿转唯一并改由 session_turns 取 occurredAt（ts 不在 bodies 源列合同内）。为什么够用：同上，" +
			"会话臂覆盖全部镜像行；ts 等值消歧在会话族由 request_id 唯一性承担（session_bodies 无重 request_id 实测）。",
	},
	"admin/unified_detail.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason: "pgBodyReader.loadOutboundBody 统一详情 outbound 三腿（hot+view 字面 + 会话兜底）。" +
			"退役后替代：删 v1 两腿，会话腿转唯一。理由同 admin/logs.go 条目。",
	},
	"domains/hooks/goal/history_store.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason: "FetchBySession 主路径已走切换层（historySessionSQL），v1 字面臂（historyV1SQL，request_id 单键）" +
			"是刻意的回退。退役后替代：删 v1 回退臂，主路径转唯一。" + bodiesSwitchRetireNote,
	},

	// ── 组 3：v1 专属读方（替代方案 ≠ 切换层）────────────────────────────
	"admin/session_bodies_batch.go": {
		Family:     familyBodies,
		ResolvesTo: "request_logs_bodies",
		Reason: "两处字面站点，替代不同：① v1BodiesByRequestIDSQL——session_summary_v2 回退路径专用" +
			"（其 turns 腿就是 v1 视图的遗留会话，bodies 腿必须同源），随 v1 表 DROP 退役，届时未镜像" +
			"遗留会话（sessions_v2.enabled=false 时代）的摘要语料消失，属已登记可接受损失，删表前 owner" +
			"在生产库复核该人群规模；② sessionBodiesByRequestIDAndTSSQL——无生产调用方（仅守形测试引用，" +
			"§5.9 历史形状钉子），DROP 时直接删除。主批量取数器（sessionBodiesByRequestIDSQL）已于" +
			"2026-10-13 固定读会话族（消费方 session_compare 的 turns 腿是会话族）。",
	},
	"admin/quality_correlations.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason: "auto-route 质量相关性分桶。★ 2026-10-13 S4 波 4 **已落地**会话族重写：" +
			"turns 腿改 canonical 源，bodies 腿经切换层读 **outbound_body**（完整上游请求对象，含 " +
			"tools/messages 键——不能读 request_body：会话臂那是 request_delta 增量数组、没有这些键，" +
			"机械沿用会静默塌桶）；tools 桶带 jsonb_typeof 守卫（22023 教训）。语义边界：无会话头" +
			"探针行按设计不进会话族（R51），分桶样本自此只含业务 auto 流量（旧实现无探针排除谓词、" +
			"探针行混入样本；本地实测 v1 auto 23,786 行里探针占 22,951）。探针维度分析若需保留，" +
			"改读 node_probe_runs 族——仍属主拍板项。DROP 时随切换层删 v1 臂。",
	},
	"admin/data_lifecycle.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason: "data-lifecycle/stats 看板两站点：其语义就是 **v1 存储体量治理**（pg_total_relation_size" +
			"('request_logs') 等）。退役后替代：端点随 v1 存储退役；若要保留看板需另做会话族体量版" +
			"（独立工作项，与 data_lifecycle blob 会话臂债同登记）。停写后 trend 两臂随窗口滑过冻结点归零" +
			"是预期行为。",
	},
	// admin/data_lifecycle_blobs.go 于 2026-10-13（S4 波 4）出列：会话臂
	// 扩展已落地（治理对象 session_bodies 族 outbound_body+双 attachments，
	// execute 目标 session_bodies_hot，增量正文保留），文件脱离 v1 族。
	"cmd/compression-bench/main.go": {
		Family:     familyBodies,
		ResolvesTo: "request_logs_bodies",
		Reason: "CLI 压缩基准工具，(request_id,ts) 等值 JOIN 读 outbound_body（切换层不投影 ts，" +
			"不能换切换层——db 注释点名的三个 ts 读方之一）。退役后替代：改读 session_bodies_hot∪" +
			"session_bodies 原生列（request_id+ts+outbound_body 齐全，ts 为正文写入时刻，与 v1 同义）。" +
			"冻结存量在 DROP 前仍可跑基准；属工具，不阻塞删表。",
	},
	"cmd/tools/backfill_session_bodies/main.go": {
		Family:     familyBodies,
		ResolvesTo: "request_logs_bodies",
		Reason: "v1→v2 回填工具本体：输入（完整 request_body/response_body 载荷）按设计只存在于 v1，" +
			"会话族没有等价物——它正是会话族这份数据的**来源**。退役后替代：无（工具使命随 v1 终结）。" +
			"本地库已实测真损失 0（无需回填）；生产删表前由 owner 复核同口径。",
	},
	"cmd/tools/validate_sessions_v2/loader.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason: "双写 parity 门的 **V1 真值臂**（hot 腿+母表腿）：它读 v1 是定义使然——把 V1 臂改读" +
			"会话族会变成自己比自己。退役后替代：工具使命随 v1 终结（parity 证据只对冻结窗有效，" +
			"WindowExceedsV1Data 已显式守窗）。",
	},
	"db/request_logs_view_schema.go": {
		Family:     familyBodiesMixed,
		ResolvesTo: "request_logs_bodies",
		Reason: "bodies 读端灰度切换层**本体**（SessionBodiesSourceSQL 的 v1 臂字面量）。" +
			"退役后替代：删除 v1 臂（返回值恒为 SessionFamilyBodiesSourceSQL），开关退化为 no-op 后移除。" +
			"为什么够用：开关默认关（v1 臂）是灰度期的回滚设计；停写后 v1 臂对会话族 turns 是错源，" +
			"会话臂是唯一正确形态，且组 1 的全部消费点已按「会话臂为常态」登记。",
	},
}

// TestV1BodiesReadersAreAssessed 是 bodies 腿的双向门。
//
// 两条方向：
//   - 实测到 bodies 读点但没登记 ⇒ **红**。这是本门当前的状态。
//   - 登记了但实测为 0 ⇒ 文件被 repoint 了（或读法变了），登记应删。
//
// 门红时的提示必须**可执行**：指出替代来源的方向，而不是只说「有问题」。
func TestV1BodiesReadersAreAssessed(t *testing.T) {
	root := repoRootFromCaller(t)
	measured := scanV1BodiesReaders(t, root)

	var unregistered, stale []string
	for f, n := range measured {
		if _, ok := v1BodiesReaders[f]; !ok {
			unregistered = append(unregistered, f+": "+strconv.Itoa(n)+" 处")
		}
	}
	for f := range v1BodiesReaders {
		if _, ok := measured[f]; !ok {
			stale = append(stale, f)
		}
	}
	sort.Strings(unregistered)
	sort.Strings(stale)

	total := 0
	for _, n := range measured {
		total += n
	}
	t.Logf("v1 bodies 读方：%d 文件 / %d 调用点（已登记 %d）",
		len(measured), total, len(v1BodiesReaders))
	// 把「当前应当是红的」写进输出。理由：本门与 S4 门、回填门一样是**故意红**的
	// 负向判据，而 CI 输出里一个不带标记的 FAIL 会被读成回归。
	// ⚠ 措辞不能含糊：它是**预期红**，但**不代表 bodies 已可删**。
	if len(unregistered) > 0 {
		t.Logf("【预期红】本门故意红：bodies 腿尚未逐点评估。红了 = 判据在拦；" +
			"全绿 = 登记表已覆盖全部 bodies 读方（那时才谈得上 request_logs_bodies 可删）。")
	}
	for f, n := range measured {
		t.Logf("  %-58s %d", f, n)
	}

	if len(unregistered) > 0 {
		t.Errorf("%d 个文件读 v1 bodies 腿但未登记 —— 在逐点评估之前，"+
			"**不能**把 request_logs_bodies 判定为可删：\n  %s\n\n"+
			"为什么这必须红而不是记一条日志：DROP 之后这些 JOIN 不会报错，"+
			"它们读到 NULL，而多数调用点写的是 COALESCE(…, '{}') —— "+
			"于是导出/摘要照常成功，只是每条消息的正文为空。"+
			"「返回 18%% 数据的查询和正常工作的查询看起来一样」在这里还要更糟一档。\n\n"+
			"处置方向：会话族有 session_bodies 表"+
			"（promote 函数见 db/db.go:3267，回填工具 cmd/tools/backfill_session_bodies）。"+
			"★ 处置指引（R48-A11 订正，旧文本「先补 helper」已过期）："+
			"bodies 源 helper **已存在**——turns 侧 SessionFamilyTurnsSourceSQL 同族，"+
			"bodies 侧是 db/request_logs_view_schema.go 的 SessionFamilyBodiesSourceSQL "+
			"（storage.session_bodies_native_read 开关，默认 v1 臂）。"+
			"逐个把 LEFT JOIN request_logs_bodies* 改指切换层前，先读 "+
			"docs/audit/2026-10-06-v1-bodies-read-path.md（§9.265）："+
			"「只改指切换层但开关仍在 v1 臂」是假迁移，登记时须写明取源路径。"+
			"★ 旧指引「先补那个 helper」已过期——helper 就是 SessionFamilyBodiesSourceSQL 本身，勿再造一个。",
			len(unregistered), strings.Join(unregistered, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("%d 条 bodies 读方登记已失效（实测 0 处）——多半是读法变了：\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}
}

// TestV1BodiesGateIsNotSilentlyVacuous 挡住「读方集合为空 ⇒ 门全绿」这种
// 最省事的假绿。
//
// 这道门当前**故意红**。它最可能的失效方式不是变绿，而是某次重构让
// scanV1BodiesReaders 扫不到东西，于是 0 个未登记、0 条失效、门全绿，
// 而 bodies 依赖一个都没被评估。⇒ 总体为 0 必须判红。
func TestV1BodiesGateIsNotSilentlyVacuous(t *testing.T) {
	root := repoRootFromCaller(t)
	measured := scanV1BodiesReaders(t, root)
	if len(measured) == 0 {
		t.Fatal("v1 bodies 读方实测为 0 个文件 —— 这不是「bodies 腿已经没人读了」，" +
			"是扫描器坏了：SSOT 推导或路径过滤出了问题。在这种状态下上面那道门会全绿，" +
			"而一个 bodies 依赖都没被评估。")
	}
	if len(v1BodiesRelations(t)) == 0 {
		t.Fatal("v1 bodies 关系名集合为空（地板断言本该拦住，见 v1BodiesRelations）")
	}
}

// TestV1BodiesScanIncludesRegisteredIndirectReaders 钉住「间接读方并入总体」本身。
//
// # 为什么这道门不能省
//
// scanV1BodiesReaders 末尾那段「把 indirectRequestLogsReaders 里 ResolvesTo 落在
// v1 bodies 的文件并进总体」是 §9.230 加的。把**它**删掉，总体从 25 文件/43 调用点
// 悄悄缩回 24/42，而 `TestV1BodiesReadersAreAssessed` **照样红**——
// 它本来就是故意红的，24 个和 25 个都不改变退出码。
// ⇒ 删掉修复之后没有任何门变绿或变红，这个修复就是**未经证实的**。
//
// 判据不能写成「总体数 ≥ 25」：那是把一个会随别的读方增减而漂的数钉死，
// 下一个改 bodies 读方的人会为了过门去调门槛。⇒ 判据是**集合关系**：
// 凡登记为间接 bodies 读方的文件，必须逐个出现在实测总体里。
func TestV1BodiesScanIncludesRegisteredIndirectReaders(t *testing.T) {
	root := repoRootFromCaller(t)
	measured := scanV1BodiesReaders(t, root)
	rels := map[string]bool{}
	for _, n := range v1BodiesRelations(t) {
		rels[n] = true
	}

	var want, missing []string
	for file, e := range indirectRequestLogsReaders {
		rel := strings.ToLower(e.ResolvesTo)
		isBodies := rels[rel] || rels[rel+"_with_current_month"] || rels[rel+"_hot"]
		if !isBodies {
			continue // 读的是 turns 族，与本门总体无关
		}
		want = append(want, file)
		if measured[file] == 0 {
			missing = append(missing, file)
		}
	}
	sort.Strings(want)
	sort.Strings(missing)
	t.Logf("登记为间接 bodies 读方的文件 %d 个：%v", len(want), want)
	if len(want) == 0 {
		t.Fatal("没有任何文件被登记为间接 bodies 读方 —— " +
			"要么 indirectRequestLogsReaders 的 bodies 登记被删了（本该有一项），" +
			"要么 v1BodiesRelations 推不出 bodies 关系名。两种都让这道门失去意义。")
	}
	if len(missing) > 0 {
		t.Errorf("这些文件登记为间接 v1 bodies 读方，但没出现在实测总体里：%v\n"+
			"⇒ scanV1BodiesReaders 末尾的「间接读方并入总体」被删掉或失效了。"+
			"它们的 v1 bodies 依赖会从退役证据里消失，而门**仍然红**"+
			"（本来就红，25 个还是 24 个都不改退出码）。", missing)
	}
}

// TestV1BodiesScanSeesJoinOnlyReaders 钉住「只有 JOIN、没有 FROM」也必须算数。
//
// # 为什么这道门不能只靠「红/绿」自证
//
// TestV1BodiesReadersAreAssessed 的判据是「登记表覆盖全部 bodies 读方」。
// 若有人把扫描模式从 `from|join` 收窄回 `from`，实测会**照样红**——
// 26 个文件变成 23 个，剩下的照样没登记。门红得像在正常工作，
// 而 `admin/session_export.go` / `admin/session_compare.go` 这两个
// **只 JOIN 不 FROM** 的读方已经悄悄从总体里消失了。
//
// 这正是本轮 §9.226 在 requestLogsReadInventory 上实测到的原缺陷
// （那里是 3 个文件对门完全不可见），同一个坑在两个门上各踩一次。
// ⇒ 必须有一条**不依赖红绿**的判据：总体里必须存在只靠 JOIN 才被看见的文件。
func TestV1BodiesScanSeesJoinOnlyReaders(t *testing.T) {
	root := repoRootFromCaller(t)
	measured := scanV1BodiesReaders(t, root)

	// 同一批文件、同样的注释剔除，只把 join 去掉。
	fromOnly := regexp.MustCompile(`(?i)\bfrom\s+(?:` +
		strings.Join(quoteAll(v1BodiesRelations(t)), "|") + `)\b`)

	// ⚠ 两个模式都**提到循环外**。第一版把 v1BodiesReadPattern(t) 写在
	// per-file 循环里，于是每读一个文件就重建一次正则（含 SSOT 推导与排序）。
	// 26 个文件不致命，但它把这条门从「毫秒」拖成「看着像卡住」——
	// 而一个让人觉得卡的门，人会先去怀疑它而不是去读它。
	fjPattern := v1BodiesReadPattern(t)

	var joinOnly []string
	for f := range measured {
		src, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		hasFrom, hasJoin := false, false
		for _, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "/*") {
				continue
			}
			if fromOnly.MatchString(line) {
				hasFrom = true
			}
			if fjPattern.MatchString(line) {
				hasJoin = true
			}
		}
		if hasJoin && !hasFrom {
			joinOnly = append(joinOnly, f)
		}
	}
	sort.Strings(joinOnly)
	if len(joinOnly) == 0 {
		t.Errorf("总体里没有任何「只 JOIN 不 FROM」的 bodies 读方 —— " +
			"这正是 §9.226 修掉的盲区形状。若这里为 0，要么扫描模式被收窄回 from-only，" +
			"要么这些读方已经改完；两者都需要有人显式确认，不能默认。")
	}
	t.Logf("只 JOIN 不 FROM 的 bodies 读方：%d 个 %v", len(joinOnly), joinOnly)
}

func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = regexp.QuoteMeta(s)
	}
	return out
}

// TestV1BodiesReadersAreDeclaredWell 挡住「登记了但说不清」。
//
// # 为什么这道门是必需的，而不是「照抄旁边那个」
//
// TestV1BodiesReadersAreAssessed 的判据是「登记表覆盖全部 bodies 读方」。
// 而 `indirectReader` 的三个字段**没有一个是 Go 编译器要求填的**：
// 加一条 `"admin/session_export.go": indirectReader{}` 就让这道门**变绿**，
// 实际评估量为零。
//
// 旁边那道 TestIndirectRequestLogsReadersAreDeclaredWell 就是干这个的
// （它还多校验了 `ResolvesTo` 必须是 `v1DirectTables` 里的真表名，
// 好让「它读的是 v1」这句话可核而不只是断言）。
// 本表复用同一个结构体 ⇒ **必须自己再写一道**，
// 否则「登记」与「填了一个空结构体」在退出码上不可区分。
//
// ⚠ 本表校验的是 **bodies** 关系名（从 SSOT 推导），不是 `v1DirectTables`：
// bodies 读方登记的 `ResolvesTo` 应当是它读的那张 bodies 表，
// 拿 turns 的集合去校它会把这个表变成一张「查不到任何东西」的表。
func TestV1BodiesReadersAreDeclaredWell(t *testing.T) {
	names := v1BodiesRelations(t)
	bodiesRel := map[string]bool{}
	for _, n := range names {
		bodiesRel[n] = true
	}
	for file, e := range v1BodiesReaders {
		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("%s：bodies 读方登记的 Reason 为空——空理由的登记等于没有登记；"+
				"这一项要写清「退役后由什么替代、为什么那个替代够用」", file)
		}
		if strings.TrimSpace(e.ResolvesTo) == "" {
			t.Errorf("%s：ResolvesTo 为空。不写它，「它读的是 v1 bodies」就是一句断言；\n"+
				"  写上表名之后，这句话可以被 v1BodiesRelations 核对。", file)
		} else if !bodiesRel[strings.ToLower(e.ResolvesTo)] {
			t.Errorf("%s：ResolvesTo = %q，不在 v1 bodies 关系名集合里。\n"+
				"  可选：%s\n"+
				"  若它其实指向别的表，应该从本表删掉，而不是改成一个不存在的表名。",
				file, e.ResolvesTo, strings.Join(names, " / "))
		}
		if strings.TrimSpace(e.Family) == "" {
			t.Errorf("%s：Family 为空。这一项用于说明它读的是哪一族（"+
				"本表的读者分不清「已评估」与「已登记但没看」）。", file)
		}
	}
}
