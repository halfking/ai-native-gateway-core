package dbinit

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 2026-11-23 L29 轮（真库空库实测）：★ 981 与 990 之间存在一处**真实的依赖倒置**
// ⇒ ★ 新装路径必然失败。
//
// ★ 依赖：★ 981 建的是**会话分析的 4 个聚合物化视图**
// （user_session_aggregates / project_task_aggregates /
//   agent_usage_aggregates / model_usage_aggregates），
// ★ 它们读 `session_summaries.total_credits_charged`，
// ★ 而**该列由 `990_stats_consistency_fixes.sql` 添加**
// （`ADD COLUMN IF NOT EXISTS total_credits_charged BIGINT NOT NULL DEFAULT 0`），
// ★ ★ 全仓只有 990 加这一列（其余命中都是引用）。
//
// ★ 实测（★ 真库空库，★ 安装器真实路径）：
//	ERROR:  column ss.total_credits_charged does not exist
//	LINE 13:     COALESCE(SUM(ss.total_credits_charged), 0)     AS extern...
// ⇒ ★ 两条通道（安装器 StartupFiles / 部署通道 files=()）**都**曾把 981 排在 990 之前。
//
// ★ 该依赖是 981 的**改写版**才引入的（★ 引入提交 78cef308f「迁移 981 按真库重写」），
// ★ 而部署脚本里 981 的注释曾写「★ 不依赖 990/991/992 的产物」⇒ ★ **注释在写下时就是错的**。
//
// ★ 生产为何没炸：★ 历史顺序巧合 —— 211 实测 `990` 记账 2026-11-19 23:21:57，
// ★ 而 981 落地时（11-19 23:21 之后、11-20 12:43 之前）990 早已 applied ⇒ 列已在。
//
// ★⇒ 所以这不是「安装器少了一个文件」，★ 而是「迁移集内部有一处前后依赖倒置，
// ★   而两条投递通道恰好共享同一个错误顺序」。
//
// ★ 本文件把两件事钉住：
//   ① 安装器通道的相对顺序（990 必须早于 981）；
//   ② **部署通道的相对顺序必须同样满足这一依赖** ⇒ ★★ 「只改一条通道」当场红
//      （★ 这一条是 L29 轮的附带产出：★ 两条通道共享同一个错误顺序，★ 而仓里
//        没有一道门比对过它们的顺序 ⇒ ★ 这就是「为什么 981 在两处都错」没人发现的原因）。
//
// ★★ **为什么本文件不写「两通道顺序必须逐条全等」的门**（★ 2026-11-23 实测结论）：
//   ★ 两条通道的投递顺序**实测存在大量历史差异**（★ 公共文件的相邻相对次序
//   ★ 有数十处不同，★ 例如 `392`/`470`、`540`/`541`、`547`/`655`、`610`/`617` …）
//   ★⇒ **「全等」是一个假不变式** ⇒ ★ 全等门会变成**假红工厂**（★ 与
//   ★ `runner_541_order_test.go` 里那段注释同一个道理：★ StartupFiles 本来就
//   ★ 不是按编号升序，★ 顺序服从依赖、★ 不服从编号）⇒ ★★ **⇒ 只能【逐依赖】钉。**

// startupDependency 是「必须在前的迁移 → 必须在后的消费者」这一对。
//
// ★ 每一对都要能说清**为什么**（★ 消费者读的是前者的哪个产物）⇒ ★
// 只有「读列/读表/读函数」这类**真实产物依赖**才进来 ⇒ ★
// 「编号顺序」不是依赖 ⇒ ★ 不进来。
var startupDependency = []struct {
	before string // 先跑
	after  string // 后跑（读 before 的产物）
	why    string
}{
	{
		before: "541_candidate_binding_scope_revision.sql",
		after:  "568_credential_priority_flag.sql",
		why:    "568 索引 candidate_binding_scope_revision（541 建的那张表）",
	},
	{
		before: "990_stats_consistency_fixes.sql",
		after:  "981_session_analytics_foundation.sql",
		why:    "981 的会话分析物化视图读 session_summaries.total_credits_charged（990 加的列）",
	},
	{
		before: "982_provider_call_attempts.sql",
		after:  "986_provider_invoice_evidence.sql",
		why:    "986 在 982 创建的 provider_call_attempts 上建立账单请求引用索引",
	},
	{
		before: "982_provider_call_attempts.sql",
		after:  "985_provider_attempt_media_usage.sql",
		why:    "985 在 982 创建的 provider_call_attempts 上补充媒体计量",
	},
	{
		before: "986_provider_invoice_evidence.sql",
		after:  "987_provider_invoice_usage_buckets.sql",
		why:    "987 在 986 创建的 provider_invoice_lines 上增加结构化汇总字段",
	},
}

func TestStartupFilesHonourDeclaredDependencies(t *testing.T) {
	for _, dep := range startupDependency {
		requireStartupOrder(t, dep.before, dep.after)
	}
}

// deployChannelStartupOrder 从部署通道脚本（scripts/apply-db-revision-sequence.sh）
// 的 `files=(...)` 数组里按出现顺序取出启动迁移文件名。
//
// ★ 为什么要单独解析部署通道：★ 迁移有**两条**投递通道
// （① 安装器的 dbinit.Runner.StartupFiles ② 部署脚本的 files=()），
// ★ 而「一条通道修好、另一条没修」是**不会自己暴露**的形态 ⇒ ★ 必须有一道门
// 把两者对**同一依赖**的判断钉在一起。
//
// ★ 重复出现取**首次**位置（★ 实测：★ 部署通道里 `800_provider_endpoint_protocols.sql`
// ★ 出现两次（★ index 158 与 173，★ 后者是重放/延后投递段）⇒ ★ 对「先后」判据
// ★ 而言首次位置才是投递起点）。
func deployChannelStartupOrder(t *testing.T) []string {
	t.Helper()
	const scriptPath = "../../../scripts/apply-db-revision-sequence.sh"
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("读取部署通道脚本失败（%s）：%v", scriptPath, err)
	}
	text := string(raw)
	start := strings.Index(text, "files=(")
	if start < 0 {
		t.Fatalf("部署通道脚本里找不到 files=( 数组（%s）", scriptPath)
	}
	end := strings.Index(text[start:], "\n)")
	if end < 0 {
		t.Fatalf("部署通道脚本里 files=( 数组没有正常闭合（%s）", scriptPath)
	}
	body := text[start : start+end]
	// ★★★ ★★ 2026-11-24：★ **解析器缺陷已修（★ 真缺陷、★ 实测坐实）** ★★★ ★★
	// ★★★ 第一版的正则 `"\$ROOT_DIR/sql/migrations/startup/([^"]+\.sql)"` 会
	// ★★★ **匹配到被注释掉的行** ⇒ ★★★ 因为它不关心行首是不是 `#` ★★★
	// ★★★ ⇒ ★★★ **实测坐实（★ 211 生产只读对账时暴露）**：★★
	// ★★★   · `666_llm_hourly_stats_direct_trigger.sql`（★ 行 635、★ 注释写明
	// ★★★     「Abandoned in favor of 667」★★）
	// ★★★   · `890_drop_request_logs_v1_family.sql`（★ 行 1974、★ 注释写明
	// ★★★     「已移出自动重放列表」、★ 因为它自带**非空闸**、★ 天然不可自动重放 ★★）
	// ★★★ 这**两条**都**不在**通道的真实投递面里 ⇒ ★★★ **真实投递面 = 355 条**
	// ★★★ （★ 而第一版解析出 357 条 ⇒ ★ **偏大 2 条** ★★★）★★
	//
	// ★★★ ⇒ ★★★ **这会连带影响什么（★ 必须说清楚 ★★★）**：★★
	// ★★★   ① 报告 §7.31 的「部署通道 356 条」这个数字 ⇒ ★★ **偏大 1 条** ★★
	// ★★★      （★ 356 那个数是**另一次口径**下的读数、★ 本轮已订正 ★★★）
	// ★★★   ② 两通道集合差异基线（★ 330 / 26 / 62 ★）⇒ ★★ **偏大** ★★
	// ★★★   ③ 覆盖门（★ 方案 C ★）的「投递面 N 条」⇒ ★★ 357 ⇒ ★ **355** ★★
	//
	// ★★★ ⇒ ★★★ **修法**：★★ **逐行解析**、★ **只取未被注释的行** ★★
	// ★★★ （★ 注释行以 `#` 开头 ⇒ ★ 跳过 ★★）★★ ⇒ ★★ ⇒ ★★ **⇒ 解析面
	// ★★★ 从此等于【通道真正会投递的文件集】** ★★★。
	//
	// ★★★ ★★ 判据：★★ 「投递面」的定义就是**通道会真正 apply 的文件集** ⇒ ★★
	// ★★ **被注释掉的条目不是投递面** ⇒ ★★ ⇒ ★★ 一条被注释的条目若被算进来 ⇒ ★★
	// ★★ **它会让「通道会重放它」这个结论凭空多出一条** ⇒ ★★ ⇒ ★★ **⇒ 判据
	// ★★ 必须是「未注释的行」** ★★。
	var names []string
	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		// ★ ★★ 注释行（★ `#` 开头 ★★）⇒ ★ **不是投递面** ★★
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// ★ 保留「行内只有这一条引号路径」的写法（★ 通道脚本的实际形态 ★）
		m := regexp.MustCompile(`^\s*"\$ROOT_DIR/sql/migrations/startup/([^"]+\.sql)"\s*$`).FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if strings.HasSuffix(m[1], ".down.sql") {
			continue
		}
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		names = append(names, m[1])
	}
	if len(names) == 0 {
		t.Fatalf("部署通道 files=() 里没解析出任何启动迁移（%s）", scriptPath)
	}
	return names
}

// ★★ 部署通道必须满足**同一条**依赖清单 ⇒ ★「只改一条通道」当场红。
//
// ★★ 缺席即跳过（★ 不是失败）：★ 两条通道投递的文件集合**本就不完全相同**
// （★ 例：★ `568_credential_priority_flag.sql` **不在部署通道的 files=() 里**
// ★ ⇒ ★ 541/568 这对依赖在部署通道**不适用**）⇒ ★★ **⇒ 「这条通道不投递它」
// ★ 是**另一个**议题（★ 缺席门／退役门／L22 的投递链议题）★ 而不是顺序门的议题
// ⇒ ★★ **⇒ 顺序门只回答「两边都投的东西，先后对不对」**。
func TestDeployChannelHonoursDeclaredDependencies(t *testing.T) {
	names := deployChannelStartupOrder(t)
	idx := make(map[string]int, len(names))
	for i, n := range names {
		idx[n] = i
	}
	for _, dep := range startupDependency {
		b, okB := idx[dep.before]
		a, okA := idx[dep.after]
		if !okB || !okA {
			t.Logf("部署通道不投递这一对（跳过）：%q（present=%v）/ %q（present=%v）—— %s",
				dep.before, okB, dep.after, okA, dep.why)
			continue
		}
		if b >= a {
			t.Errorf("部署通道顺序错：%q 在 %d，但必须早于 %q 的 %d（%s）",
				dep.before, b, dep.after, a, dep.why)
		}
	}
}
