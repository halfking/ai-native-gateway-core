package dbinit

// 2026-11-23 L33 三轮（L34 取证轮）：★ 「部署通道不认安装器的账」⇒ 真库 hermetic 实测
// ⇒ ★ 把这条口径**钉成可执行断言**（★ 否则下一次改通道的人无从知道它存在）。
//
// ★★★ 实测事实（本轮真库空库、★ 一次性容器、★ 零生产写）★★★：
//
//	臂 D（新装库 ⇒ 直接跑部署通道）：
//	  INSTALL_EXIT=0（149s / 576 表）★ schema_migrations = 396 行 / 最大版本 999
//	  ★ gateway_db_revision_sequences = **表不存在（0）**
//	  DEPLOY_EXIT=3（18s）★ 中止于 563_session_summary_trigger_on_hot.sql
//	    ⇒ ERROR:  relation "request_logs_hot" does not exist
//
//	臂 E（反证：★ 把「安装器确实应用过、且部署通道也投递」的文件按部署通道自己的
//	      marker 口径种进 gateway_db_revision_sequences ⇒ 跑同一条通道）：
//	  ★ 330 行台账 ⇒ ★ 跳过 53 条（already applied）★ 应用 6 条（部署通道独有）
//	  DEPLOY_EXIT=3 ⇒ ★ **换了个地方炸**：★ 661_session_summary_token_ratio_reassert.sql
//	    ⇒ ERROR:  cannot change name of view column "id" to "source"
//
// ★★★ ⇒ ⇒ 两句结论（★ 都必须被钉住）★★★：
//
//	① **台账不互通**：★ 部署通道的跳过判据**只**看 `public.gateway_db_revision_sequences`
//	   （★ 文件名 + 内容指纹），★ **从不**看安装器那张 `public.schema_migrations`
//	   ⇒ ⇒ 新装库里它**对「安装器已应用了什么」零知识** ⇒ ⇒ 只能从零重放。
//	② **两通道的文件集合本就不相同**：★ 安装器 392 / 部署通道 356 / 交集 330
//	   ⇒ **仅部署通道有 26 条**（★ 含 L22 的 055–058 teams 族）★
//	   ⇒ **仅安装器有 62 条**（★ 含安装器专属资产 session_turns_hot_bootstrap.sql）★
//	   ⇒ ⇒ 这 26 条在**空库新装出来的库上从未存在** ⇒ ⇒ 部署通道跑它们时可能撞上
//	   ★★ **新装 schema 假设不成立**（★ 实测 661 就是这样炸的）★★。
//
// ★★ ⇒ ★★ **所以「让部署通道也认安装器的账」是必要条件、★ 但不是充分条件** ★★
// ⇒ ★★ 报告 §7.31.5 的结论：★ 方案 A（★ 触及生产升级路径 ⇒ owner 轴）★ 与
// ★★ 「部署通道独有文件也要在空库上可应用」必须**同批** ★★。
//
// ★★ 本文件的两道门是**棘轮**：★★ 它们钉的是**今天的现状**（★ 「两本账不互通」
// ★★ + ★ 集合差异的规模）★★ ⇒ ★★ **若有人落了方案 A（★ 部署通道改读
// ★★ `schema_migrations`），★ 本门会红** ⇒ ★★ **这是刻意的**：★★ 请连同本门
// ★★ 与报告 §7.31 一起更新（★ 别让口径悄悄漂掉）★★。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// deployChannelScriptPath = 部署通道脚本相对本测试文件的路径。
const deployChannelScriptPath = "../../../scripts/apply-db-revision-sequence.sh"

// TestDeployChannelSkipsOnlyByItsOwnLedger 把「部署通道的跳过判据只认自己那本账」
// 钉成可执行断言。
//
// ★ 判据：★ 脚本里凡是提到 `schema_migrations` 的**非注释行**，★ 都必须处在
// ★ **引号里**（★ 即：要么是产物探针的 SQL 字符串、★ 要么是文件名）★
// ★ ⇒ ⇒ **脚本从不把安装器那张账当成「跳过/已应用」的判据** ★★。
func TestDeployChannelSkipsOnlyByItsOwnLedger(t *testing.T) {
	raw, err := os.ReadFile(deployChannelScriptPath)
	if err != nil {
		t.Fatalf("读取部署通道脚本失败（%s）：%v", deployChannelScriptPath, err)
	}
	text := string(raw)
	if !strings.Contains(text, "public.gateway_db_revision_sequences") {
		t.Fatalf("★ 部署通道脚本里找不到它自己的权威台账 public.gateway_db_revision_sequences ⇒ ★ 判据已失效（★ 别按本门结论改代码）")
	}
	// ★ 判据：★ 脚本**不得**用 psql_query / psql_stream 去查安装器那张账
	// （★ `public.schema_migrations`）⇒ ★ ⇒ 它的「跳过/已应用」判据只认自己那本
	// ★ `public.gateway_db_revision_sequences` ★★。
	//
	// ★ 为什么按「调用点」而不是按「文本里出现过 schema_migrations」来判：★
	// ★ 脚本里出现 `schema_migrations` 的**合法**用法是**产物探针**
	// （★ `ledger_artifact_probe "<file>" "<SQL 表达式>"` ★ ⇒ 它只是**读产物**、
	// ★ 不参与「跳过」判定）★ ⇒ ★ 按文本判会误报 ⇒ ★ ⇒ 必须按**调用点**判 ★★。
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if !strings.Contains(line, "psql_query") && !strings.Contains(line, "psql_stream") {
			continue
		}
		// ★ 实参窗口＝本行 ＋ 下一行（★ 覆盖跨两行的调用写法 ★）。
		window := line
		if i+1 < len(lines) {
			window += "\n" + lines[i+1]
		}
		if !strings.Contains(window, "schema_migrations") {
			continue
		}
		t.Errorf("★ 部署通道脚本第 %d 行用 psql_query/psql_stream 查了安装器那张账（public.schema_migrations）：%s\n"+
			"★ ⇒ 这意味着它可能把安装器的账当成了「跳过/已应用」的判据 ⇒ ★ 而今天的实测是：\n"+
			"★   新装库上部署通道会全量重放、★ 并在第 13 个文件中止（★ 报告 §7.31.2 臂 D）\n"+
			"★ ⇒ ★ 若这是**刻意**落地的方案 A（★ 部署通道改读 schema_migrations）★\n"+
			"★ ⇒ ★ 请连同本门与报告 §7.31 一起更新（★ 刻意留红、★ 别让口径悄悄漂掉）",
			i+1, strings.TrimSpace(window))
	}
}

// TestChannelFileSetBaseline 把两条投递通道的**文件集合差异**钉成基线。
//
// ★ 为什么钉数字：★ 「仅部署通道有的 26 条」正是 L22（★ 055–058 teams 族 ★）
// ★ 那类「投递链缺失」的同族 ⇒ ★ ★ 它们在空库新装出来的库上**从未存在** ★★
// ⇒ ★ ★ 集合一变 ⇒ ★ 要么多了一批新装库跑不起来的文件、★ 要么少了一批 ⇒ ★
// ★ ★ 两种都**必须**有人复核 ⇒ ★ ★ 所以这里钉住数字、★ 变了就红 ★★。
func TestChannelFileSetBaseline(t *testing.T) {
	installer := map[string]bool{}
	for _, name := range NewRunner("citus", "user", "db", "/tmp/sql").StartupFiles {
		installer[name] = true
	}
	deploy := map[string]bool{}
	for _, name := range deployChannelStartupOrder(t) {
		deploy[name] = true
	}
	both, onlyDeploy, onlyInstaller := 0, 0, 0
	var onlyDeployNames []string
	for name := range deploy {
		if installer[name] {
			both++
			continue
		}
		onlyDeploy++
		onlyDeployNames = append(onlyDeployNames, name)
	}
	for name := range installer {
		if !deploy[name] {
			onlyInstaller++
		}
	}
	const (
		// ★★★★ ★★★ 2026-11-24：★ **基线 332/26/62 → 331/25/63** ★★★ ★★★
		//   起因（★ 两个**互相独立**的变化叠加 ★★）：
		//
		//   ★ ① **上游新增迁移 983**（★ `983_request_metrics.sql` ★）：★
		//     ★ 它同时落【部署通道 files=()】（★ 行 2843 ★）与【安装器
		//     ★ StartupFiles】⇒ ⇒ **交集 +1**（★ 331 → 332 ★★）★★
		//     ★ ⇒ ★ 两侧【单通道独有】的缺口清单**一条都没动** ★★ ⇒ ★
		//     ★ ⇒ ★ 「新装库跑不起来的文件」这一类【★ 没有变化 ★】★★
		//
		//   ★ ② ★★ **解析器缺陷被实测坐实并修掉**（★ 见
		//     ★ `runner_990_order_test.go` 的 `deployChannelStartupOrder` 注释 ★★）
		//     —— ★★ 第一版解析器**把被注释掉的行也当成投递面** ★★
		//     ⇒ ★★ 实测（★ **211 生产只读对账**时暴露 ★★）★★ 通道脚本里有**两条**
		//     ★★ 被注释掉的投递条目 ★★：
		//     · `666_llm_hourly_stats_direct_trigger.sql`（★ 注释写明
		//       「Abandoned in favor of 667」★ ★★ 且**该文件在仓里根本不存在**
		//       ⇒ ★ **幽灵引用** ★★）；
		//     · `890_drop_request_logs_v1_family.sql`（★ 注释写明
		//       「已移出自动重放列表」、★ 因为它自带**非空闸**、★ 天然不可自动重放 ★★）。
		//     ⇒ ★★ 890：**只在安装器侧投递**（★ `runner.go` 第 1208 行 ★）⇒ ★
		//     ★ ⇒ 它从「仅部署」**移出**、★ 变成「仅安装」⇒ ★★ ⇒ ★
		//     ★ **交集 −1、仅部署 −1、仅安装 +1** ★★ ⇒ ★★ ⇒ ★
		//     ★ **「新装库跑不起来的文件」这一类** ⇒ ★ **没有新增 ★** ★★
		//     （★ ★ 反而**更准**了：★ 此前那个「仅部署有 890」是**假的** ★★）★★
		//     ★ 666：**两边都不投递**（★ 仓里无此文件 ★★）⇒ ★ 它**不进任何一侧**
		//     ⇒ ★★ ⇒ ★ 交集、两侧差数**均不受它影响** ★★
		//
		//   ★ ⇒ ★★ ①＋② 叠加：**331 → 332 → 331**、★ **26 → 26 → 25**、
		//   ★ ★ **62 → 62 → 63** ★★ ⇒ ★★ ⇒ ★ **⇒ 本次更新是【★ 如实反映事实 ★】、
		//   ★ ★ 不是「把红抹掉」 ★★」
		//
		// ★ ★★ 顺带订正报告 §7.31 的三个数字（★ 本轮已回填 ★★）：
		//   ★ §7.31.2/§7.31.3 实测的「部署通道 356 条」⇒ ★ **355 条** ★
		//   ★ §7.31.4 的集合差异「330 / 26 / 62」⇒ ★ **330 / 25 / 63** ★
		//   ★ §7.32 的覆盖门「投递面 357 条」⇒ ★ **355 条** ★
		//   ★ ★★ 而**合并上游 983 后**（★ 2026-11-24 ★）⇒ ★ 投递面 **356 条** ★★
		wantBoth          = 331
		wantOnlyDeploy    = 25
		wantOnlyInstaller = 63
	)
	if both != wantBoth || onlyDeploy != wantOnlyDeploy || onlyInstaller != wantOnlyInstaller {
		t.Errorf("★ 两条投递通道的文件集合变了：★ 交集 %d（期望 %d）/ 仅部署通道有 %d（期望 %d）/ 仅安装器有 %d（期望 %d）\n"+
			"★ ⇒ ★ 请复核报告 §7.31.4（★ 集合差异的含义）★ 并更新本基线\n"+
			"★ ⇒ ★ 仅部署通道有的文件（★ %d 条）：%v",
			both, wantBoth, onlyDeploy, wantOnlyDeploy, onlyInstaller, wantOnlyInstaller,
			len(onlyDeployNames), onlyDeployNames)
	}
}

// TestDeployChannelDeliverySurfaceFilesExist 把「投递面里的每个文件都真实存在」
// 钉成可执行断言。
//
// ★★★ 这道门为什么存在（★ 2026-11-24 实测坐实的一个真缺陷 ★★★）：★★
// ★★ 通道脚本里留着一条**被注释掉的**投递条目：★★
// ★★   `# "$ROOT_DIR/sql/migrations/startup/666_llm_hourly_stats_direct_trigger.sql"` ★★
// ★★ 而**那个文件在仓里根本不存在**（★ 现存的是 `666_orchestration_and_stats_tables.sql`、
// ★★ 是**另一个文件** ★★）⇒ ★★ ⇒ ★★ **这是一条【幽灵引用】** ★★ ⇒ ★★ ⇒ ★★
// ★★ 而它之所以长期没被发现：★ ★ **第一版解析器把注释行也当成投递面** ★★
// ★★ ⇒ ★★ 「投递面」这个概念**混进了不投递的东西** ⇒ ★★ ⇒ ★★
// ★★ **⇒ 「投递面」的定义必须是【通道真正会 apply 的文件集】** ★★。
//
// ★★★ ★★ 判据 ★★★：★★
// ★★ ① 投递面（★ 未注释的条目 ★）里的每个文件，★ 必须在
// ★★    `sql/migrations/startup/` 下**真实存在** ⇒ ★★ 否则通道跑到那儿就炸
// ★★    （★ 通道脚本第 3266 行：`[[ -f "$file" ]] || { … exit 4; }` ★★）；★★
// ★★ ② **被注释掉的**投递条目，★ 若引用了**不存在的文件** ⇒ ★ ★ **红** ★★
// ★★    （★ 理由：★ 它是一条**将来的雷** —— ★ 哪天有人「取消注释」它、
// ★★    ★ 而文件又没被补回来 ⇒ ★★ 通道当场 exit 4 ★★）★★。
func TestDeployChannelDeliverySurfaceFilesExist(t *testing.T) {
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
	// ★ 真实投递面（★ 未注释的行 ★）
	live := regexp.MustCompile(`^\s*"\$ROOT_DIR/sql/migrations/startup/([^"]+\.sql)"\s*$`)
	// ★ 被注释掉的投递条目（★ 整行注释 ★）
	commented := regexp.MustCompile(`^\s*#\s*"\$ROOT_DIR/sql/migrations/startup/([^"]+\.sql)"\s*$`)
	// ★ 路径里的通配（★ `prepare_runtime_sql` 那类、★ 不在 files=() 里 ★）
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		isComment := strings.HasPrefix(trimmed, "#")
		var name string
		if isComment {
			m := commented.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name = m[1]
		} else {
			m := live.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name = m[1]
		}
		path := filepath.Join("..", "..", "..", "sql", "migrations", "startup", name)
		if _, statErr := os.Stat(path); statErr == nil {
			continue
		}
		if isComment {
			// ★ ★★ 被注释的幽灵引用 ⇒ 红（★ 是将来的雷 ★★）★★
			t.Errorf("★ 部署通道脚本里**被注释掉**的投递条目 %s 指向的文件**在仓里不存在**（★ %s）★★\n"+
				"★ ⇒ ★ 这是一条【★ 幽灵引用 ★】⇒ ★ 哪天有人取消注释它、★ 而文件没补回来 ⇒ ★★\n"+
				"★ ⇒ ★ 通道会在那一条上**直接 exit 4**（★ 见通道脚本第 3266 行的存在性断言 ★★）★★\n"+
				"★ ⇒ ★ 处置：★ ① 把这个文件名改成真实存在的文件 ★★／★ ② 或把这条注释**连同它的历史说明**一起删掉 ★★\n"+
				"★ ⇒ ★ （★ 注意：★ 本条**不**判「注释掉」这件事本身 ★★ —— ★ 退役的迁移**本来**就该被注释掉 ★★）",
				name, path)
			continue
		}
		// ★ ★★ 真实投递面里的文件不存在 ⇒ 红（★ 通道跑到那儿会 exit 4 ★★）★★
		t.Errorf("★ 部署通道的**真实投递面**里有文件 %s 在仓里**不存在**（★ %s）★★\n"+
			"★ ⇒ ★ 通道跑到那一条会**直接 exit 4**（★ 通道脚本第 3266 行：★ `[[ -f \"$file\" ]] || exit 4` ★★）★★",
			name, path)
	}
}
