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
		// ★ 2026-11-23：★ 交集 330 → 331（★ 其余两数不变）★★
		//   起因：★ 「逐次供应商调用事实」迁移 982（★ provider_call_attempts ★）
		//   同时落【部署通道 files=()】与【安装器 StartupFiles】⇒ ⇒ 交集 +1 ★。
		// ★ 语义复核（★ 按本门注释的判据，★ 不是「调大基线了事」★）：
		//   ★ 本门钉的是「★ 集合差异 ★」⇒ ★ 「仅部署 26」与「仅安装器 62」
		//   【★ 两条【单通道独有】的缺口清单 ★】★ ⇒ ★ 本轮【★ 一条都没动 ★】★
		//   ⇒ ★ ⇒ 「新装库跑不起来的文件」这一类【★ 没有变化 ★】★★
		//   ⇒ ★ ⇒ 交集 +1 是【★ 两个通道【都】能投递同一个文件 ★】的正常结果
		//   （★ 此前 981/990 等大量迁移本就是双通道共有 ★）★★
		//   ⇒ ★ ⇒ 本次更新是【★ 如实反映事实 ★】、★ 不是「把红抹掉」★★
		//
		// ★★ 2026-11-24（合并轮十）：★ 交集 332 → 336 ★★（★ **只动交集、★
		//   「仅部署 26」与「仅安装器 62」一字未动 ★**）★★
		//   起因（★ **实测构成、★ 逐个文件核过 ★**）：★ 合并轮十把上游
		//   `origin/main`（★ 该提交自己的口径是 332）合进来时，★ **teams 族
		//   那 4 件也在本分支里、★ 且已被登记进【两条通道】** ⇒ ★★ 实测
		//   交集 = **330（初始基线）＋ 982（provider_call_attempts）＋
		//   teams 族 4 件（★ `918_teams_table`／`925_user_teams`／
		//   `973_api_keys_team_id`／`986_team_budgets`）＋ 983（request_metrics）**
		//   ＝ **336** ★★（★ 上游 332 ＝ 330＋982＋983 ⇒ ★ **差的正是 teams
		//   族那 4 件** ⇒ ★ 上游那边没有 teams 族、★ 本分支有 ★★）★★。
		//   ★ 补记：★ `bb2f02718`（★ 并行车道）**早先**就把基线写成 334
		//   （★ 它的注释写的正是「teams 族重编号后双通道登记的预期效应」★）⇒
		//   ★★ **合并时上游 `d380dbb41` 的 332 覆盖了它** ⇒ ★★  ⇒ ★ 这就是
		//   「★ 合并后基线与实际不符 ★」的成因（★ **不是** 新装缺口变化 ★）★★。
		// ★ 语义复核（★ 同上、★ 仍按本门判据 ★）：★ 「仅部署 26」与
		//   「仅安装器 62」**一字未动** ⇒ ★★ ⇒ ★ **两本账不互通的现状没变**、
		//   ★ **新装库跑不起来的文件清单没变** ⇒ ★★ ⇒ ★ 本次更新同样是
		//   【★ 如实反映事实 ★】、★ 不是「把红抹掉」 ★★。
		wantBoth          = 336
		wantOnlyDeploy    = 26
		wantOnlyInstaller = 62
	)
	if both != wantBoth || onlyDeploy != wantOnlyDeploy || onlyInstaller != wantOnlyInstaller {
		t.Errorf("★ 两条投递通道的文件集合变了：★ 交集 %d（期望 %d）/ 仅部署通道有 %d（期望 %d）/ 仅安装器有 %d（期望 %d）\n"+
			"★ ⇒ ★ 请复核报告 §7.31.4（★ 集合差异的含义）★ 并更新本基线\n"+
			"★ ⇒ ★ 仅部署通道有的文件（★ %d 条）：%v",
			both, wantBoth, onlyDeploy, wantOnlyDeploy, onlyInstaller, wantOnlyInstaller,
			len(onlyDeployNames), onlyDeployNames)
	}
}
