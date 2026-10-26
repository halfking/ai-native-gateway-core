// R75 轮 · P11「巡检基线快路」的三道静态门。
//
// 背景：§143.19~§143.29 的成本归因把「池被占满」的元凶钉在
// scripts/252-monitor/ursm-snapshot-health.sh 的 base_buckets
// （每小时扫 7 天源表，生产实测 mean 235,016ms / max 591,621ms / 2.9GB 物理读）。
// 迁移 892 建了「每日 slot 汇总表」作为落点，巡检脚本也据此多了一条快路。
//
// ★★★ 但 2026-10-22（R75 轮）实测发现：**落 892 本身并不能治掉元凶**，
//
//	   而且照现状直接落上去**会把巡检从「慢但对」变成「快但盲」**。三处真缺陷：
//
//	① 【回填的时间预算余量只有 2× ⇒ 该留余量】
//	   ★★ **注意：本条曾被本轮自己的生产实测推翻过一次** ——
//	   原判词是「892 必然超时 ⇒ 整条迁移回滚」，而生产 252 上把**本回填的同形态**
//	   查询逐条实测：8 天 · hour = 70,136 ms、8 天 · minute = 79,335 ms
//	   ⇒ 合计 **≈149 s** ⇒ ★ **落在原 300 s 预算之内** ⇒ 「必然超时」不成立。
//	   ⇒ ⇒ 缺陷① 因此**降级**为「余量仅 2×」⇒ 抬高预算到 30min 是【余量保险】，
//	     而**不是**修一个必然发生的回滚。★ 门因此钉的是「预算」这条决定本身。
//	   ★ 本地 PG 17.10 实测（决定性）：
//	     A：DO 块内两条语句各 1.5s、块级超时 2s ⇒ 第二条被取消
//	        ⇒ 块内多条语句【共享同一个语句预算】；
//	     B：块【内部】 SET LOCAL statement_timeout='30s' ⇒ 仍被取消
//	        ⇒ 外层计时器已武装，块内改 GUC 无效；
//	     C：在 DO【之前】 SET LOCAL ⇒ 通过
//	        ⇒ 抬高必须发生在那之前；
//	     D：拆成两个独立 DO 块 ⇒ 各自有独立预算。
//	   ⇒ 修法 = 在 DO 之前抬高（实验 C 的形态）。
//
//	② 【没有任何地方会再调用那个填充函数 ⇒ 汇总表停更 ⇒ 巡检静默失明】
//	   892 只在首次部署时回填一次；之后那张表再也不动。
//	   ⇒ 快路读到的基线一天比一天少 ⇒ 第 8 天左右【一行都读不到】
//	   ⇒ 全部桶判 NO-BASELINE ⇒ 巡检静默失明（与 892 自己的失败模式同型）。
//	   ⇒ 修法 = 新增每日/每 6 小时的刷新作业（scripts/252-monitor/ursm-snapshot-slot-refresh.sh）。
//
//	③ 【巡检的基线覆盖探针判据方向反了】
//	   原判据 `min(day) <= 今天-基线天数` 只问「有没有【足够老】的数据」，
//	   而快路真正需要的是「有没有【足够新】的数据」
//	   ⇒ 表一旦停更（缺陷 ②），min(day) 不随时间前进 ⇒ 探针依然判「覆盖已足」
//	   ⇒ 巡检继续走快路 ⇒ 读到一天比一天少的基线 ⇒ 静默失明。
//	   ⇒ 修法 = 判据改为「窗口内的每一天都有数据」（count(DISTINCT day)）。
//
// ★ 三道门都是【静态】的（不需要 DSN ⇒ CI 可跑）——
//
//	这正是 473 同族的教训：realdb 门本地/CI 从不执行 ⇒ 缺口能活很久才被发现。
package ursm

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	mig892     = "../../sql/migrations/startup/892_ursm_snapshot_slot_daily.sql"
	mig892Down = "../../sql/migrations/startup/892_ursm_snapshot_slot_daily.down.sql"

	patrolScript = "../252-monitor/ursm-snapshot-health.sh"
	refreshScrip = "../252-monitor/ursm-snapshot-slot-refresh.sh"
	cronCanon    = "../252-monitor/etc.cron.d.pg17"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	return string(b)
}

// stripSQLComments 去掉 SQL 注释（-- 行注释与 /* */ 块注释）。
//
// ★★ 为什么这道门必须有它（★ 本轮真的被它抓到过一次）：
//
//	down 迁移里那段「说明为什么原来写错了」的注释，**本身就写着**那个错误的签名
//	`refresh_ursm_snapshot_slot_daily(integer);`
//	⇒ 不剥注释的话，正向断言会被注释里的文本**假满足**、反向断言会被注释里的文本**误报**。
//	⇒ ⇒ 仓库纪律「断言 SQL 文本前必须剥注释」在这里不是洁癖，是**判据正确性**的前提。
func stripSQLComments(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "--"):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				i = len(s)
			} else {
				i += j
			}
			out.WriteByte('\n')
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				i = len(s)
			} else {
				i += 2 + j + 2
			}
			out.WriteByte('\n')
		default:
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

// timeoutSeconds 把 `SET LOCAL statement_timeout = '<v>'` 的字面量折算成秒。
// 支持 '30s' / '30min' / '1800'（秒）/ '1h'。
func timeoutSeconds(t *testing.T, literal string) int {
	t.Helper()
	literal = strings.TrimSpace(literal)
	literal = strings.Trim(literal, "'")
	re := regexp.MustCompile(`^(\d+)\s*(ms|s|min|h)?$`)
	m := re.FindStringSubmatch(literal)
	if m == nil {
		t.Fatalf("看不懂的 statement_timeout 字面量：%q", literal)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("statement_timeout 字面量不是整数：%q", literal)
	}
	switch m[2] {
	case "ms":
		return n / 1000
	case "min":
		return n * 60
	case "h":
		return n * 3600
	default:
		return n
	}
}

var reTimeout = regexp.MustCompile(`SET LOCAL statement_timeout\s*=\s*'([^']+)'`)

// =============================================================================
// 门 1：892 的回填必须【在 DO 之前】把 statement_timeout 抬到 300s 之上
// =============================================================================

func TestP11R75Migration892BackfillTimeoutIsRaisedBeforeTheDOBlock(t *testing.T) {
	// ★ 先剥注释：本迁移的注释里**引用了**多个 statement_timeout 字面量
	//   （实验记录），不剥注释的话注释也能满足正向断言。
	body := stripSQLComments(readFile(t, mig892))

	doIdx := strings.Index(body, "DO $backfill$")
	if doIdx < 0 {
		t.Fatalf("%s 里找不到 `DO $backfill$` ⇒ 判据失效（文件被改名或重构了？）", mig892)
	}

	// ★ 只看 DO 之前的那些 SET LOCAL。
	pre := body[:doIdx]
	var best int
	found := false
	for _, m := range reTimeout.FindAllStringSubmatch(pre, -1) {
		sec := timeoutSeconds(t, m[1])
		if sec > best {
			best = sec
		}
		if sec > 300 {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s 的回填 DO 块【之前】没有把 statement_timeout 抬到 300s 以上（最大只到 %ds）\n"+
			"★ 本门钉的是【预算留足余量】这条决定（★ **不是**「300s 必然超时」——\n"+
			"  **那句话已被本轮的生产实测推翻**，见下面两行）。\n"+
			"★ 生产 252 实测（2026-10-22，与本回填**同形态**的查询）：\n"+
			"    8 天 · hour = 70,136 ms ；8 天 · minute = 79,335 ms ⇒ 合计 **≈149 s**\n"+
			"  ⇒ 落在原 300 s 预算之内 ⇒ ★★ **「300s 必然超时」不成立** ⇒ 原 892 本就能落。\n"+
			"★ 那为什么仍要抬？因为 149 s 是【一次】的读数，而同族查询在生产上曾观测到\n"+
			"  max 591,621 ms ⇒ ★ **2× 余量在共享忙库上偏薄** ⇒ 抬到 30min 是【余量保险】。\n"+
			"★ 抬高必须写在 DO【之前】：本地 PG 17.10 实测 —— 块内多条语句共享同一个语句预算，\n"+
			"  块内 SET LOCAL 无效（外层计时器已武装），DO 之前抬高才有效。",
			mig892, best)
	}

	// ★ 兜底：不得让 DO 之后的语句沿用抬高值去跑别的东西（那会让整条迁移失去超时保护）。
	post := body[doIdx:]
	for _, m := range reTimeout.FindAllStringSubmatch(post, -1) {
		if timeoutSeconds(t, m[1]) > 300 {
			t.Errorf("%s 的 DO 块【之后】还有 statement_timeout='%s' ⇒ 抬高没有只作用于回填\n"+
				"★ 抬高必须【窄】：只覆盖回填那一段，后面的语句仍应有 300s 的保护。",
				mig892, m[1])
		}
	}
}

// =============================================================================
// 门 2：down 迁移必须删【真实存在】的那个重载
// =============================================================================

func TestP11R75Migration892DownDropsTheSignatureThatActuallyExists(t *testing.T) {
	// ★ 先剥注释：否则注释里出现的签名会同时造成假满足与误报。
	body := stripSQLComments(readFile(t, mig892Down))

	const want = "DROP FUNCTION IF EXISTS public.refresh_ursm_snapshot_slot_daily(integer, text)"
	if !strings.Contains(body, want) {
		t.Errorf("%s 里没有 %q\n"+
			"★ 892 明确【只建一个重载】(integer, text)（文件头自述：两个都带默认参数时 f(1) 报 not unique，\n"+
			"  本地 PG 17.10 实测）⇒ 单参数版本根本不存在 ⇒ DROP 它是静默空操作\n"+
			"  ⇒ 回滚后【函数残留、表已删】⇒ 刷新作业每次都会报「relation does not exist」。",
			mig892Down, want)
	}
	// ★ 反向自证：那个不存在的单参数签名必须**不再**出现（否则又退化成静默空操作）。
	const bogus = "refresh_ursm_snapshot_slot_daily(integer);"
	if strings.Contains(body, bogus) {
		t.Errorf("%s 里仍有 %q ⇒ 它指向**不存在的**单参数重载 ⇒ 静默空操作 ⇒ 函数会残留在库里",
			mig892Down, bogus)
	}
}

// =============================================================================
// 门 3：巡检的基线覆盖探针必须问「窗口内每一天都有吗」
// =============================================================================

// coverageProbeFragment 是巡检脚本与刷新作业**共用**的那段覆盖计数。
// 两边必须逐字同款 ⇒ 否则会出现「刷新脚本以为够了、巡检脚本以为不够」的错位。
const coverageProbeFragment = "count(DISTINCT day)::text FROM public.ursm_snapshot_slot_daily"

func TestP11R75PatrolCoverageProbeAsksAboutTheWindowNotAboutAge(t *testing.T) {
	body := readFile(t, patrolScript)

	if !strings.Contains(body, coverageProbeFragment) {
		t.Errorf("%s 里没有 %q\n"+
			"★ 巡检的基线覆盖判据必须是「窗口内【每一天】都有数据」\n"+
			"  （count(DISTINCT day) over [今天-基线天数, 今天) == 基线天数）。\n"+
			"★ 旧判据 `min(day) <= 今天-基线天数` 只问「有没有【足够老】的数据」——\n"+
			"  而快路需要的是「有没有【足够新】的数据」⇒ 表一旦停更，min(day) 不前进 ⇒\n"+
			"  探针依然判「覆盖已足」⇒ 巡检继续走快路 ⇒ 读到一天比一天少的基线 ⇒ ★★ 静默失明。",
			patrolScript, coverageProbeFragment)
	}

	// ★ 决策必须是「覆盖天数 >= 基线天数」，不能是任何基于 min(day) 的比较。
	if !strings.Contains(body, `[ "$base_cov" -ge "$BASELINE_DAYS" ]`) {
		t.Errorf("%s 里没有 `[ \"$base_cov\" -ge \"$BASELINE_DAYS\" ]` ⇒ 决策口径不是「覆盖天数 vs 基线天数」",
			patrolScript)
	}
	const oldDecision = `[ "$base_min" \\< "$need_min" ]`
	if strings.Contains(body, oldDecision) {
		t.Errorf("%s 里仍有旧的 min(day) 决策 %q\n"+
			"★ 那个判据只看「够不够老」⇒ 表停更时依然成立 ⇒ ★★ 静默失明", patrolScript, oldDecision)
	}

	// ★ 窗口下界/上界必须同时在场（只查其中一半等于没查）。
	for _, frag := range []string{
		"AND day >= ((now() AT TIME ZONE current_setting('TimeZone'))::date",
		"AND day <  (now() AT TIME ZONE current_setting('TimeZone'))::date",
	} {
		if !strings.Contains(body, frag) {
			t.Errorf("%s 里没有 %q ⇒ 覆盖窗口不是闭合的 [今天-基线天数, 今天)", patrolScript, frag)
		}
	}
}

// =============================================================================
// 门 4：刷新作业必须存在、必须真的调用那个函数、必须响亮失败
// =============================================================================

func TestP11R75SlotRefreshJobExistsAndCallsTheFillFunction(t *testing.T) {
	body := readFile(t, refreshScrip)

	if !strings.Contains(body, "refresh_ursm_snapshot_slot_daily") {
		t.Fatalf("%s 里没有 refresh_ursm_snapshot_slot_daily ⇒ 它不刷新任何东西", refreshScrip)
	}
	// ★ 必须**先确认函数在**再调用：函数不存在时静默跳过 = 汇总表停更 = 静默失明。
	if !strings.Contains(body, "to_regprocedure") {
		t.Errorf("%s 里没有 to_regprocedure ⇒ 函数不存在时不会响亮失败\n"+
			"★ 静默跳过是最坏的失败形态：汇总表停更 ⇒ 巡检静默失明，而外部看不出来", refreshScrip)
	}
	if !strings.Contains(body, "exit 3") {
		t.Errorf("%s 里没有 exit 3 ⇒ 「量具不可用」没有独立的退出码（0/1/3 分级契约）", refreshScrip)
	}
	// ★ 必须有「刷新后复核」：不能假定刷新成功。
	if !strings.Contains(body, "coverage_after") {
		t.Errorf("%s 里没有 coverage_after ⇒ 刷新后不复核覆盖 ⇒ 可能「刷了 0 行还报绿」", refreshScrip)
	}
	// ★ 语句超时必须被放宽（应用角色 llm_gateway 库上带 statement_timeout=30s）。
	if !strings.Contains(body, "statement_timeout") {
		t.Errorf("%s 里没有放宽 statement_timeout ⇒ 应用角色的 30s 会让每次刷新都超时失败", refreshScrip)
	}
	// ★ 不得静默：cron 行里不能出现 >/dev/null 2>&1（只看**真正的任务行**）。
	for _, line := range refreshCronLines(t) {
		if strings.Contains(line, "/dev/null 2>&1") {
			t.Errorf("刷新作业的 cron 行被静默了：\n%s\n"+
				"★ 本作业【会写库】⇒ 一次静默失败 = 汇总表停更 = 巡检静默失明", line)
		}
	}
}

// =============================================================================
// 门 5：刷新作业必须被 cron 登记（473 同族：建了却忘了接线）
// =============================================================================

// cronTaskLineRE 匹配一条**真正的** cron 任务行（与 cron_registration_test.go 同款口径）：
// 去掉注释与空行后，首字段是分钟数，且行里含 `root`。
//
// ★★ 为什么本门**必须**用它、而不是 `strings.Contains(body, 路径)`：
//
//	★ 变异 M4 把那一行**注释掉**（行首加 `# MUTATED:`）⇒ 路径字符串仍在文件里
//	  ⇒ 用 Contains 的判据**照样绿** ⇒ 门没有牙（M4 实测暴露）。
//	⇒ ⇒ 「路径出现在文件里」≠「它被调度了」。
//	  ⇒ 必须解析出**真正的任务行**再看路径在不在里面。
var cronTaskLineRE = regexp.MustCompile(`^\s*\S+\s+\S+\s+\S+\s+\S+\s+\S+\s+root\s+`)

// refreshCronLines 返回正典 cron 文件里真正调度该刷新作业的任务行。
func refreshCronLines(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readFile(t, cronCanon), "\n") {
		if !cronTaskLineRE.MatchString(line) {
			continue
		}
		if strings.Contains(line, "/opt/scripts/ursm-snapshot-slot-refresh.sh") {
			out = append(out, line)
		}
	}
	return out
}

func TestP11R75SlotRefreshJobIsRegisteredInCron(t *testing.T) {
	lines := refreshCronLines(t)
	if len(lines) == 0 {
		t.Fatalf("%s 里没有**真正的** cron 任务行调度 /opt/scripts/ursm-snapshot-slot-refresh.sh\n"+
			"★ 473 同族：建了却忘了接线 ⇒ 作业从未被运行 ⇒ 汇总表停更 ⇒ 巡检静默失明。\n"+
			"★ 而且该 cron 文件是**整文件覆盖**部署的 ⇒ 漏一条等于下次部署把它删掉。\n"+
			"★ ★ 注意判据口径：这里找的是**任务行**，不是「路径字符串出现过」——\n"+
			"  注释掉那一行等于把任务删了，而路径字符串仍然留在文件里。", cronCanon)
	}
	if len(lines) > 1 {
		t.Errorf("%s 里有 %d 行在调度刷新作业 ⇒ 期望恰好 1 行（重复调度 = 重复写库）\n%s",
			cronCanon, len(lines), strings.Join(lines, "\n"))
	}

	// ★ 两个脚本的覆盖计数口径必须逐字同款（否则会错位）。
	refresh := readFile(t, refreshScrip)
	if !strings.Contains(refresh, coverageProbeFragment) {
		t.Errorf("%s 里没有 %q ⇒ 刷新作业与巡检脚本的覆盖口径不一致\n"+
			"★ 两边必须问同一个问题，否则会出现「刷新脚本以为够了、巡检脚本以为不够」。",
			refreshScrip, coverageProbeFragment)
	}
}

// =============================================================================
// 门 6：PSQL_CMD 注入点的契约必须【写在脚本里】
// =============================================================================

// TestP11R75PatrolPSQLCmdInjectionContractIsDocumented
//
// ★ 为什么这道门存在（★ 本轮真的踩了一次，踩了很久）：
//
//	de() 的命令形态是
//	  ${PSQL_CMD:-podman exec ... psql -U ... -d ... -tAX -c} "$1"
//	⇒ `-c` 写在【默认值】里面 ⇒ 一旦注入 PSQL_CMD，整个默认值被替换掉
//	⇒ ⇒ **注入的 PSQL_CMD 必须自带结尾的 `-c`**。
//
//	★ 违约时的报错是 psql 的
//	    extra command-line argument "SELECT coalesce(to_regclass(...)..."
//	⇒ ★★ 那个报错**完全不像**「你少写了一个 -c」——
//	  它看起来像 SQL 里多了个什么、或者引号坏了 ⇒ 会把人引到完全错误的方向。
//
//	⇒ ⇒ 所以契约必须**写在脚本里**（不能只活在 e2e 脚本的用法里）。
func TestP11R75PatrolPSQLCmdInjectionContractIsDocumented(t *testing.T) {
	body := readFile(t, patrolScript)

	// ① 默认形态：`-c` 必须在默认值内部（与 e2e 脚本「调用方自带 -c」的用法一致）。
	if !strings.Contains(body, `-tAX -c} "$1"`) {
		t.Errorf("%s 里没有 `-tAX -c} \"$1\"` ⇒ 注入点形态变了\n"+
			"★ 若把 -c 挪到默认值【外面】，则注入方【不得**再自带 -c ⇒ "+
			"  而 scripts/audit/verify_892_patrol_e2e.sh 现在自带了 -c ⇒ 会变成 `psql -c -c <SQL>`。\n"+
			"  ⇒ 两种形态都行，但**必须与 e2e 脚本的用法一致**，且必须写进注释。", patrolScript)
	}

	// ② 契约必须写在注释里（否则下一个踩坑者还会踩）。
	if !strings.Contains(body, "必须自带结尾的 -c") {
		t.Errorf("%s 里没有把「注入的 PSQL_CMD 必须自带结尾的 -c」这条契约写进注释\n"+
			"★ 违约时的报错是 psql 的「extra command-line argument」——"+
			"它完全不像「少写了一个 -c」⇒ 会把人引到 SQL 语法的错误方向上。", patrolScript)
	}
}
