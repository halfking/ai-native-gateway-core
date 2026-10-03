// R89-EK（220 号）：`is_routable` 的每一项准入条件，在 `unavailable_reason`
// 里都必须有对应分支 —— 否则运维会看到「凭据被挡住，但没有任何原因」。
//
// ## 为什么要这道门
//
// `v_routable_credential_models` 的 `is_routable` 是一个**十二项 AND 合取**
// （:17-19 逐字读过），而 `unavailable_reason`（:20-40 的 CASE）是**给人看的
// 排除原因**。二者是**同一个判定**的两种表达：合取说「不能路由」，CASE 说「为什么」。
// ⇒ 任何一项合取没有对应的 CASE 分支，就是「拦了但说不清」——
// 这类缺陷不会报错、不会让请求变慢，只会让排障的人多花几十分钟。
//
// ## 本轮实测坐实的那一处（P2）—— `health_status` 的 `warning`
//
//	合取第 11 项：COALESCE(c.health_status,'unknown') = ANY(ARRAY['healthy','unknown'])
//	CASE 只有：   WHEN (c.health_status = 'unreachable' AND c.health_checked_at > now()-1h)
//	                       THEN 'recent_probe_unreachable'
//
//	`credentials.health_status` 的 CHECK 约束允许四个值
//	（`chk_credentials_health_status`：`unknown|healthy|warning|unreachable`），
//	其中 **`warning` 落在合取的排除集里，却没有任何 CASE 分支**。
//
//	`warning` 确实会被写进去（不是纸面取值）：
//	  admin/provider_cred_lifecycle.go:349 —— 运维点「健康检查」时 chat 探针非 200，
//	  即 `healthStatus = "warning"`（同函数 :226/:238/:291 写 "unreachable"，
//	  :297/:344 写 "healthy"）。
//
//	⇒ 一次「健康检查失败但没有硬不可达」的探测，会让该凭据的**全部绑定**退出路由，
//	  而 `unavailable_reason` 返回 **NULL**（CASE 一路落到 ELSE NULL，:39）。
//
//	⚠️ 同一列还有第二重缺口，已一并登记：即便值是 `unreachable`，CASE 分支也带
//	**一小时时间窗**（`health_checked_at > now()-'01:00:00'`），而合取**没有时间窗**
//	⇒ 超过一小时的 `unreachable` 行，**仍然被合取拦住、但原因同样为 NULL**。
//
// ## 与 219 号那条缺口的关系（不要合并叙述）
//
// 待裁决 85 说的是「批量修复只清 DB 侧、不清进程/Redis 侧」；
// 本条说的是「**即使 DB 侧全清了，`health_status` 这一项仍可能没被清**
// （`handleRoutingBlockedFix` 不重置 `health_status`），而且它被拦住时没有原因」。
// 两者是同一条运维链上的**两处**断点，修一处不能修另一处。
//
// ## 本门是「契约钉桩」，不是「检测活缺陷」──
// 缺口已被如实登记（`warning` 声明为 Gap + 写明理由），所以现在**是绿的**；
// 谁在 CASE 里补上 `warning` 分支却不更新登记表，本门会转红，强制同步
// —— 否则下一个接手的人会把「已登记的缺口」当成「已覆盖」。
package routeguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readFileForTest 按 RoutingViewFiles 返回的**仓库根相对**路径读文件。
// 显式拼 `../..` 而不是依赖调用方的工作目录：判据的输入路径必须无歧义，
// 否则换个目录跑就变成「文件读不到 → 跳过」，那比红更坏。
func readFileForTest(relRoot string) (string, error) {
	b, err := os.ReadFile(filepath.Clean(filepath.Join("..", "..", relRoot)))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// valueGate 是「合取里按取值设闸、且该列有合法取值域」的一道闸。
//
// ⚠️ 刻意只收**取值域型**的闸（availability/quota/health），
// 布尔型与子查询型（`p.enabled`、`pm.available`、`nps` 那一项）不收：
// 它们的 CASE 分支都是无条件覆盖的（已逐行核对），收进来只会变成噪声。
type valueGate struct {
	// Column 是合取里出现的列（含表别名）。
	Column string
	// Excluded 是「让该项为假」的合法取值。
	Excluded []string
	// CoveredBy 是「CASE 里确实有分支」的证据子串，逐条必须出现在 CASE 段。
	// 用证据子串而不是裸取值，是因为 quota 那条是用拼接
	// （`('quota_'::text || c.quota_state)`）覆盖的，裸字面量不出现。
	CoveredBy []string
	// Gaps 是「被合取排除、但 CASE 里没有分支」的取值。
	// **每个 gap 的字面量必须不出现**在 CASE 段 —— 有人补了却没更新登记表就红。
	Gaps []string
	// GapWhy 在 Gaps 非空时必填。
	GapWhy string
	// Notes 记这个闸的非显然性质（时间窗等），只供人读。
	Notes string
}

// valueGates 是 220 号逐行读 `sql/objects/views/v_routable_credential_models.sql`
// 得出的登记。
var valueGates = []valueGate{
	{
		Column: "c.availability_state",
		// CHECK 约束 chk_credentials_availability_state_check 允许六值，
		// 合取要求 = 'ready'，其余五个即被排除。
		Excluded: []string{"cooling", "rate_limited", "auth_failed", "unreachable", "suspended"},
		CoveredBy: []string{
			"c.availability_state = 'cooling'",
			"c.availability_state = 'rate_limited'",
			"c.availability_state = 'auth_failed'",
			"c.availability_state = 'unreachable'",
			"c.availability_state = 'suspended'",
		},
		Gaps:  []string{},
		Notes: "五值全覆盖（:26-30），无需额外动作。",
	},
	{
		Column:   "c.quota_state",
		Excluded: []string{"permanently_exhausted", "balance_exhausted", "periodic_exhausted"},
		// CASE 用拼接覆盖（:31 `('quota_'::text || c.quota_state)`），
		// 所以证据是拼接式而不是三个裸字面量。
		CoveredBy: []string{"|| c.quota_state"},
		Gaps:      []string{},
		Notes:     "三值全覆盖（:31）。",
	},
	{
		Column:    "c.health_status",
		Excluded:  []string{"warning", "unreachable"},
		CoveredBy: []string{"c.health_status = 'unreachable'"},
		Gaps:      []string{"warning"},
		GapWhy: "🔴 220 号 P2：`warning` 是合法取值（chk_credentials_health_status 允许）" +
			"且确有生产者（admin/provider_cred_lifecycle.go:349，运维点健康检查时" +
			"chat 探针非 200），落在合取第 11 项的排除集里，**却没有任何 CASE 分支**" +
			"⇒ 被合取拦住时 unavailable_reason 落到 ELSE NULL（:39），" +
			"运维看到「不可路由」却没有原因。**本轮未改视图**：无真库可验证 SQL，" +
			"且该视图被 27 个文件重新定义（含 328/332 等迁移），改它要先定" +
			"「改哪一份 + 迁移 checksum 怎么处理」（184 号已记录 73 条 >412 的迁移被拒）。",
		Notes: "⚠️ 第二重缺口：`unreachable` 的分支带**一小时时间窗**" +
			"（:32 `health_checked_at > now()-'01:00:00'`），而合取**没有时间窗**" +
			"⇒ 超过一小时的 unreachable 行同样「被拦住但无原因」。" +
			"这条不单列 Gap（值本身有分支），但必须与上面的 warning 一起修。",
	},
}

// caseSection 抽出 `unavailable_reason` 那个 CASE 表达式的文本。
//
// ⚠️ 第一版锚在 `"END) AS unavailable_reason"` 上，实测**在 6/18 个文件上失效**
// （如 `sql/migrations/startup/460_v_routable_credential_models_periodic_exhausted.sql:79`
// 写的是 `END AS unavailable_reason`，**没有那个右括号** —— 规范对象文件有，
// 迁移文件没有）。锚得太死 ⇒ 把「排版不同」误判成「视图形态变了」
// ⇒ 门的报错会变成噪声，而**噪声会让人习惯性忽略它**。
// ⇒ 现在锚在 `AS unavailable_reason`（两种写法都含），再往回找最近的 `CASE`。
//
// 找不到时返回空串：调用方会因「证据子串找不到」而红，不会静默通过。
func caseSection(src string) string {
	end := strings.Index(src, "AS unavailable_reason")
	if end < 0 {
		return ""
	}
	start := strings.LastIndex(src[:end], "CASE")
	if start < 0 {
		return ""
	}
	return src[start:end]
}

// conjunctSection 抽出 `is_routable` 合取所在的文本。
func conjunctSection(src string) string {
	if i := strings.Index(src, "AS is_routable"); i >= 0 {
		return src[:i]
	}
	return ""
}

// TestRoutableReasonColumnSurvivesEveryRedefinition 对**全部**重新定义该视图的文件
// 断言一件事：`unavailable_reason` 这一列**不能被任何一次重定义弄丢**。
//
// ⚠️ 为什么这一条覆盖全部文件，而下一条只查规范文件：
//
//	第一版把「值级覆盖」也套在全部 18 个文件上，实测**把 8 个历史迁移判红**
//	（131 / 327 / 328 / 332 / 334…）—— 因为它们是**各时期的视图快照**：
//	写法没有 `c.` 前缀、且早于 `health_status` 闸被引入。
//	形态不同是**正常的**，把它们判红只会制造噪声，而**噪声会让人习惯性忽略这道门**
//	（与「一道会误报正确代码的门比没有门更坏」同源）。
//
//	⇒ 拆成两条：**列的存在性**要对全部文件断言（重定义丢列是真实且危险的失效），
//	  **值的覆盖度**只对规范对象文件断言（它才是现行形态的权威源）。
//
// definesRoutableView 判断一个文件是否**真的**（重）定义该视图。
//
// ⚠️ 为什么需要它：RoutingViewFiles 是按「文件内容含 is_routable 字样」选文件的，
//
//	所以**注释里提一句**也会被算成「重定义」。220 号实测扫描面 18 个文件里
//	有 **3 个只提及不定义**：
//	  sql/migrations/domain/334_cmb_billing_align_credential_plan.sql
//	  sql/migrations/manual/20260719_add_volcano_glm52.sql
//	  sql/migrations/startup/672_local_first_title_summary_routing.sql
//	（第三个我最初怀疑是「installer 会真执行的迁移却不受门保护」，**核实后证伪** ——
//	  它 :10 只在注释里提 is_routable。）
//	⇒ 「列的存在性」这种断言只能对**真定义**的文件做，否则会对着注释报错。
//	⚠️ 刻意**不改** RoutingViewFiles：它同时被 R87-f 的禁用列守卫使用，
//	收紧它的扫描面等于**削弱一道既有门**的覆盖面 —— 那不是本轮该顺手做的事。
func definesRoutableView(src string) bool {
	return strings.Contains(src, "VIEW public.v_routable_credential_models") ||
		strings.Contains(src, "VIEW v_routable_credential_models")
}

func TestRoutableReasonColumnSurvivesEveryRedefinition(t *testing.T) {
	files, err := RoutingViewFiles("../..")
	if err != nil {
		t.Fatalf("枚举路由视图文件: %v", err)
	}
	// 下限按逐文件实测取紧：220 号在当前 HEAD 上量到扫描面内**真定义**该视图的
	// 文件 15 个（扫描面返回 18 个，其中 3 个只提及不定义，见 definesRoutableView）。
	// 掉一个就红（枚举退化了 / 形态判定退化了）。
	const floorFiles = 15
	if len(files) < floorFiles {
		t.Fatalf("只枚举出 %d 个路由视图文件，低于下限 %d —— 枚举退化了，"+
			"此时「每次重定义都核对过」是空断言", len(files), floorFiles)
	}

	var definers, mentionOnly []string
	for _, path := range files {
		b, readErr := readFileForTest(path)
		if readErr != nil {
			t.Errorf("%s: %v", path, readErr)
			continue
		}
		if !definesRoutableView(b) {
			mentionOnly = append(mentionOnly, path)
			continue
		}
		definers = append(definers, path)
		if !strings.Contains(b, "unavailable_reason") {
			t.Errorf("%s：重新定义了 v_routable_credential_models，但**没有** unavailable_reason 这一列 —— "+
				"重定义若真的少一列，要么报错、要么（配合 DROP）把「为什么不可路由」"+
				"这一信息整个丢掉。请确认这是有意为之，并同步更新本文件顶部的说明。", path)
		}
	}
	if len(definers) < floorFiles {
		t.Fatalf("扫描面内只有 %d 个文件**真的**重定义了该视图，低于下限 %d —— "+
			"形态判定退化成了「提及即定义」", len(definers), floorFiles)
	}
	t.Logf("扫描面内 %d 个文件：真定义 %d 个、只提及不定义 %d 个 %v",
		len(files), len(definers), len(mentionOnly), mentionOnly)
	t.Logf("⚠️ 已知覆盖边界：另有 3 份 schema dump 也**含该视图定义**，但不在扫描面内：" +
		"deploy/sql/schemas/baseline/01-schema.sql、installer/cmd/llm-gw-installer/embeddata/01-schema.sql、" +
		"sql/schema/01-schema.sql（各 :19108，同一份 dump 的副本）。是否漂移**本轮无结论**。")
}

// TestRoutableReasonCoversEveryExcludedValue 是**值级**覆盖的下限 + 契约钉桩：
//
//  1. 登记表里每一列都必须**真的出现在合取里**（防登记表漂移/拼写错）；
//  2. 每个 CoveredBy 证据子串**必须出现在 CASE 段**（删分支即红）；
//  3. 每个 Gap 取值的字面量**必须不出现**在 CASE 段
//     （有人补了分支却没更新登记表即红 —— 这是钉桩的关键方向）；
//  4. Gaps 非空时 GapWhy 必填（否则「没做」无法与「有意」区分）。
//
// ⚠️ **只核对规范对象文件**（理由见上一条函数的注释）：值级覆盖是「现行形态」的属性，
//
//	历史迁移是快照、不适用。
func TestRoutableReasonCoversEveryExcludedValue(t *testing.T) {
	b, readErr := readFileForTest(CanonicalView)
	if readErr != nil {
		t.Fatalf("读规范视图 %s: %v", CanonicalView, readErr)
	}
	conj := conjunctSection(b)
	cs := caseSection(b)
	if cs == "" {
		t.Fatalf("%s：找不到 unavailable_reason 的 CASE 段 —— 规范视图形态变了，"+
			"本门判据已不适用，请重新核对（不要静默跳过）", CanonicalView)
	}
	if conj == "" {
		t.Fatalf("%s：找不到 AS is_routable —— 规范视图形态变了", CanonicalView)
	}

	// 下限：登记的取值域型闸数（实测 3：availability / quota / health）。
	const floorGates = 3
	if len(valueGates) < floorGates {
		t.Fatalf("登记表只有 %d 道取值域型闸，低于下限 %d", len(valueGates), floorGates)
	}

	for _, g := range valueGates {
		if !strings.Contains(conj, g.Column) {
			t.Errorf("登记表里的列 %q 不在 is_routable 合取里 —— "+
				"要么登记表过期，要么合取改了（两种都必须人工确认）", g.Column)
		}
		for _, ev := range g.CoveredBy {
			if !strings.Contains(cs, ev) {
				t.Errorf("登记表说 %q 在 CASE 里被覆盖，但 CASE 段里找不到证据 %q。"+
					"要么分支被删了，要么证据子串过期了（请改证据，不要改声明）", g.Column, ev)
			}
		}
		for _, gap := range g.Gaps {
			if strings.Contains(cs, "'"+gap+"'") {
				t.Errorf("登记说 %q 在 CASE 里**没有**分支，但 CASE 段里已经出现了它 —— "+
					"视图被修了而登记表没跟上。请把 %q 从 Gaps 移到 CoveredBy 并补上证据子串，"+
					"否则下一个接手的人会以为缺口还在", g.Column, gap)
			}
		}
		if len(g.Gaps) > 0 && strings.TrimSpace(g.GapWhy) == "" {
			t.Errorf("登记 %q 有未覆盖的取值 %v，却没有写 GapWhy —— "+
				"「没做」必须能区分「有意」与「遗漏」", g.Column, g.Gaps)
		}
	}
}
