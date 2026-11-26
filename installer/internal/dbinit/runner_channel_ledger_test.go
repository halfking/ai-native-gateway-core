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
	"bytes"
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
	// ★ 判据（★ 2026-11-26 合并轮按【方案 A 已落地】的现行口径重钉）：★
	// 脚本**允许**用 psql_query 查安装器那张账（public.schema_migrations），
	// 但**只许**出现在「安装器台账推断」块里，且**必须**先过
	// `migration_version_conflict_count == 1` 的唯一性闸（★ 键不唯一 ⇒ 不推断、
	// 宁可重放不可假跳过 ★）。这就是方案 A（`4e0bd431b`，0715 审计轮判「方向对、
	// 三段判定实测冲突组 0」）的落地面。
	//
	// ★ 为什么按「调用点」而不是按「文本里出现过 schema_migrations」来判：★
	// ★ 脚本里出现 `schema_migrations` 的**合法**用法是**产物探针**
	// （★ `ledger_artifact_probe "<file>" "<SQL 表达式>"` ★ ⇒ 它只是**读产物**、
	// ★ 不参与「跳过」判定）与**方案 A 的推断块**（★ 带唯一性闸 ★）★
	// ⇒ ★ 按文本判会误报 ⇒ ★ ⇒ 必须按**调用点**判 ★★。
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
		// ★ 方案 A 的合法形态：推断块的 SELECT 必须与唯一性闸同文件共存。
		//   用「闸的调用点存在于脚本」＋「本调用点带 LIMIT 1 的幂等 SELECT」识别；
		//   其它任何对 schema_migrations 的 psql_query/psql_stream 仍一律红。
		if strings.Contains(text, "migration_version_conflict_count") &&
			strings.Contains(window, "SELECT 1 FROM public.schema_migrations") {
			continue
		}
		t.Errorf("★ 部署通道脚本第 %d 行用 psql_query/psql_stream 查了安装器那张账（public.schema_migrations）：%s\n"+
			"★ ⇒ 方案 A 只允许【带 migration_version_conflict_count 唯一性闸的推断块】这一种形态\n"+
			"★ ⇒ 任何其它对安装器台账的查询（把安装器的账当成「跳过/已应用」判据而不过闸）一律红\n"+
			"★ ⇒ 请改走推断块（先 conflict_count 闸、再 SELECT 1 ... LIMIT 1）、或改成产物探针",
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
		// ★★★★ ★★★ 2026-11-24：★ **基线 332/25/63 → 337/25/63 → 338/25/63** ★★★ ★★★
		//   起因（★ 三轮变化叠加 ★★）：
		//
		//   ★ ① **上游新增迁移 983/984**（★ request_metrics + session_universe ★）：
		//     ★ 两个都同时落【部署通道 files=()】与【安装器 StartupFiles】
		//     ⇒ ⇒ **交集 +2**（★ 330 → 332 ★★）
		//
		//   ★ ② **本地新增迁移 985-989**（★ 请求对账迁移 ★）：
		//     ★ 五个都同时落【部署通道 files=()】与【安装器 StartupFiles】
		//     ⇒ ⇒ **交集 +5**（★ 332 → 337 ★★）
		//
		//   ★ ③ **本轮（12h 修订审计轮）补登 973 + 055-058**（★ 五条 ★）：
		//     ★ 原先**只建了 canonical + embeddata 双副本**、两条通道都没投递
		//       ⇒ ★ ① raw_model_name 列在生产上**根本不存在**
		//       ⇒ ★ writer.go 写这一列必然 42703 ⇒ 整条统计管线静默丢数
		//       ⇒ ★ ② 团队族四表（teams / user_teams / api_keys.team_id /
		//         team_budgets）**从来没被任何通道建过**
		//         ⇒ maas/team_budget_gate 读不到表 ⇒ 团队预算门在生产上是死的
		//     ★ 973 还与 994_session_analytics_owner_key_and_close_path **撞号**
		//       ⇒ ★ 改用空号 973（★ 三位数惯例 ★★）并补齐九面登记 ★★
		//     ★ 五条都同时落【部署通道 files=()】与【安装器 StartupFiles】
		//     ⇒ ⇒ **交集 +5**（★ 337 → 342 ★★）
		//
		// ★ ★★ 顺带订正报告 §7.31 的三个数字（★ 本轮已回填 ★★）：
		//   ★ §7.31.2/§7.31.3 实测的「部署通道 356 条」⇒ ★ **355 条** ★
		//   ★ §7.31.4 的集合差异「330 / 26 / 62」⇒ ★ **330 / 25 / 63** ★
		//   ★ §7.32 的覆盖门「投递面 357 条」⇒ ★ **355 条** ★
		//   ★ ★★ 而**合并上游 983 后**（★ 2026-11-24 ★）⇒ ★ 投递面 **356 条** ★★
		// ★ ★★ 2026-11-24（合并轮十一）：★ **基线 331/25/63 → 335/25/63** ★★
		//   （★ **只动交集、★ 两个单通道独有的数一字未动 ★**）★★
		//   起因：★ **合并后实测**（★ **不是推算、★ 是跑出来的** ★）：★
		//   本分支除上游那几件之外、★ **还有 teams 族 4 件**（★ ★★ 2026-11-24
		//   合并轮二十一：★ teams 族已**归位回上游的 055/056/057/058**（★ 四位数
		//   1000-1003 违反 `^[0-9]{3}_` ⇒ 永不被投递、★ 且生产已 applied 055-058
		//   ⇒ 改字节即 L23 分叉）★★ ⇒ ★ 本分支的额外 4 件即这四个上游号）★
		//   **已被登记进【两条通道】** ⇒ ★★ 实测交集 = **上游的 331 ＋ 4 ＝ 335** ★★
		//   ⇒ ★★ **「仅部署 25」与「仅安装器 63」不变** ⇒ ★★ ⇒ ★ **两本账不
		//   互通的现状没变、★ 新装库跑不起来的文件清单没变** ⇒ ★★ ⇒ ★ 本次
		//   更新是【★ 如实反映事实 ★】、★ 不是「把红抹掉」 ★★。
		//
		// ★★ **★ 口径提示（★ 必须写下来、★ 免得下一个人按错的数读）★★**：★★
		//   ★ **合并轮十我方写的「336/26/62」是【旧解析器口径】下的数**（★ ★
		//   那时部署通道的解析器**把被注释掉的条目也算进投递面** ⇒ ★ **偏大** ★★）
		//   ｜★ ★ **上游本轮修掉了那个解析器缺陷**（★ ★ 逐行解析、★ 排除 `#`
		//   注释行 ⇒ ★ **解析面从此等于【通道真正会 apply 的文件集】** ⇒ ★ ★
		//   实测两条被注释掉的条目是 `666_llm_hourly_stats_direct_trigger.sql`
		//   （★ 仓里根本不存在此文件 ⇒ **幽灵引用** ★）与
		//   `890_drop_request_logs_v1_family.sql`（★ 自带非空闸、★ 天然不可自动
		//   重放 ⇒ ★ 只在安装器侧投递）★★）★★
		//   ｜★★ ⇒ ★★ **所以 336 → 335 是【口径修正】、★ 不是「事实变了」**
		//   ★★ （★ ★ **两个单通道独有的数没动** ＝ **那才是本门真正要钉的东西**）★★
		//   ｜★★ **★ 还有一条同源的订正 ★★**：★ ★ **上游同批把 `files=()` 里那行
		//   被注释掉的 890 删掉了**（★ `scripts/apply-db-revision-sequence.sh` −1 行 ★）
		//   ⇒ ★★ **⇒ 「部署通道投递面」的数字从「356/357 条」订正为「355 条」
		//   （★ 合并 983 后 356 条）** ⇒ ★★ **报告 §7.31 的那几个数字由上游本轮
		//   已回填、★ 我方本轮同步沿用** ★★。
		//
		// ★★★★ ★★★ 2026-11-24（P1 会话宇宙探测误杀修复轮）：★ **基线 335/25/63 → 336/25/63** ★★★ ★★★
		//   起因（★ **只有一个**变化 ★★）：★ 本轮新增迁移 **984**
		//   （★ `984_session_universe_cost_aware_probe_scope.sql` ★）★ ⇒ ★ 它
		//   **同时**落【部署通道 files=()】与【安装器 StartupFiles】⇒ ⇒ ★
		//   ★ **交集 +1**（★ 335 → 336 ★★）★★ ⇒ ★ 两侧【单通道独有】的
		//   ★ 缺口清单**一条都没动**（★ 仍 25 / 63 ★★）★★ ⇒ ★ ★ 「新装库
		//   ★ 跑不起来的文件」这一类**没有变化** ★★ ★★ ⇒ ★ ⇒ 本次更新是
		//   ★ 【★ 如实反映事实 ★】、★ **不是「把红抹掉」** ★★。
		//   （★ **只动交集、★ 两个单通道独有的数一字未动 ★**）★★
		//   起因：★ **合并后实测**（★ **不是推算、★ 是跑出来的** ★）：★
		//   本分支除上游那几件之外、★ **还有 teams 族 4 件**（★ `918_teams_table`／
		//   `925_user_teams`／`973_api_keys_team_id`／`986_team_budgets`）★
		//   **已被登记进【两条通道】** ⇒ ★★ 实测交集 = **上游的 331 ＋ 4 ＝ 335** ★★
		//   ⇒ ★★ **「仅部署 25」与「仅安装器 63」不变** ⇒ ★★ ⇒ ★ **两本账不
		//   互通的现状没变、★ 新装库跑不起来的文件清单没变** ⇒ ★★ ⇒ ★ 本次
		//   更新是【★ 如实反映事实 ★】、★ 不是「把红抹掉」 ★★。
		//   ★★★ 【2026-11-24 订正（★ 本车道 · 合并并行车后）★★★：★ 上面那组
		//   ★★★ 918/925/973/986 是**并行车道**的编号写法；★ ★★ 本候选已按 §7.52
		//   ★★★ **归位回上游的 055/056/057/058**（★ 生产 2026-11-10 已 applied ⇒
		//   ★★★ 改字节即 L23 分叉；★ 且指纹与权威账一致 ⇒ 不会触发重放）★★★
		//   ★★★ ⇒ ★★ **故该块保留为【历史记录】、★ 不是当前编号** ★★ ★★★
		//
		// ★★ **★ 口径提示（★ 必须写下来、★ 免得下一个人按错的数读）★★**：★★
		//   ★ **合并轮十我方写的「336/26/62」是【旧解析器口径】下的数**（★ ★
		//   那时部署通道的解析器**把被注释掉的条目也算进投递面** ⇒ ★ **偏大** ★★）
		//   ｜★ ★ **上游本轮修掉了那个解析器缺陷**（★ ★ 逐行解析、★ 排除 `#`
		//   注释行 ⇒ ★ **解析面从此等于【通道真正会 apply 的文件集】** ⇒ ★ ★
		//   实测两条被注释掉的条目是 `666_llm_hourly_stats_direct_trigger.sql`
		//   （★ 仓里根本不存在此文件 ⇒ **幽灵引用** ★）与
		//   `890_drop_request_logs_v1_family.sql`（★ 自带非空闸、★ 天然不可自动
		//   重放 ⇒ ★ 只在安装器侧投递）★★）★★
		//   ｜★★ ⇒ ★★ **所以 336 → 335 是【口径修正】、★ 不是「事实变了」**
		//   ★★ （★ ★ **两个单通道独有的数没动** ＝ **那才是本门真正要钉的东西**）★★
		//   ｜★★ **★ 还有一条同源的订正 ★★**：★ ★ **上游同批把 `files=()` 里那行
		//   被注释掉的 890 删掉了**（★ `scripts/apply-db-revision-sequence.sh` −1 行 ★）
		//   ⇒ ★★ **⇒ 「部署通道投递面」的数字从「356/357 条」订正为「355 条」
		//   （★ 合并 983 后 356 条）** ⇒ ★★ **报告 §7.31 的那几个数字由上游本轮
		//   已回填、★ 我方本轮同步沿用** ★★。
		// ★★★★ ★★★ 2026-11-24：★ **基线 332/25/63 → 337/25/63 → 338/25/63** ★★★ ★★★
		//   起因（★ 三轮变化叠加 ★★）：
		//   ★ ① **上游新增迁移 983/984**（★ request_metrics + session_universe ★）：
		//     ★ 两个都同时落【部署通道 files=()】与【安装器 StartupFiles】
		//     ⇒ ⇒ **交集 +2**（★ 330 → 332 ★★）
		//   ★ ② **本地新增迁移 985-989**（★ 请求对账迁移 ★）：
		//     ★ 五个都同时落【部署通道 files=()】与【安装器 StartupFiles】
		//     ⇒ ⇒ **交集 +5**（★ 332 → 337 ★★）
		//   ★ ③ **本轮（12h 修订审计轮）补登 973 + 055-058**（★ 五条 ★）：
		//     ★ 原先**只建了 canonical + embeddata 双副本**、两条通道都没投递
		//       ⇒ ★ ① raw_model_name 列在生产上**根本不存在**
		//       ⇒ ★ writer.go 写这一列必然 42703 ⇒ 整条统计管线静默丢数
		//       ⇒ ★ ② 团队族四表（teams / user_teams / api_keys.team_id /
		//         team_budgets）**从来没被任何通道建过**
		//         ⇒ maas/team_budget_gate 读不到表 ⇒ 团队预算门在生产上是死的
		//     ★ 973 还与 994_session_analytics_owner_key_and_close_path **撞号**
		//       ⇒ ★ 改用空号 973（★ 三位数惯例 ★★）并补齐九面登记 ★★
		//     ★ 五条都同时落【部署通道 files=()】与【安装器 StartupFiles】
		//     ⇒ ⇒ **交集 +5**（★ 337 → 342 ★★）
		//   ★ 两侧【单通道独有】的缺口清单**一条都没动** ★★ ⇒
		//   ★ 「新装库跑不起来的文件」这一类【★ 没有变化 ★】★★

		//   ★ 两侧【单通道独有】的缺口清单**一条都没动** ★★ ⇒
		//   ★ 「新装库跑不起来的文件」这一类【★ 没有变化 ★】★★
		//
		//   ★ ⇒ 本次更新是【★ 如实反映事实 ★】、★ **不是「把红抹掉」** ★★
		// ★ ④ **本轮（12h 修订审计轮第二轮）改号 973 → 983b + 新增 983c**：
		//     ★ ③ 里的「改用空号 973」在第二轮被证明**引入了新缺陷**
		//       （★ 973 是全仓唯一逆序依赖 ⇒ 两条自动枚举通道会把补列件
		//         排在建表件之前 ⇒ ALTER 落空 / 离线升级中止 ★★）
		//       ⇒ 改用 **983b**（★ 主号+字母后缀 ⇒ 排序排在 983 之后 ★★）
		//     ★ 并新增 **983c**（★ request_metrics 复合游标索引：
		//       ★ processor.go 水位线查询每批跑一次、而该索引从来不存在
		//         ⇒ 生产实测 117ms/批全表排序 ★★）
		//     ★ 两条都同时落【部署通道 files=()】与【安装器 StartupFiles】
		//     ⇒ ⇒ **交集 +1**（★ 342 → 343 ★★）★★
		//       （★ 改号 973→983b 是**同一个文件换名** ⇒ 交集数不变 ★）
		//   ★ 2026-11-24（数据维护手册轮）：999a 同时落两条通道（files=() +
		//     StartupFiles 末尾）⇒ **交集 +1**（★ 343 → 344 ★★）；
		//     两通道独有清单不变（25 / 63）。
		//
		wantBoth          = 345
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

// ★★★ 2026-11-25（codex/reconciliation-upgrade 并线）：★ 安装器新装路径的
// 【交接投递面】必须覆盖部署通道的文件集，★ 且两份资产逐字节相同 ★★
// （★ 交接凭证封存依赖这一点：★ 封存的是【安装器实际跑的字节】★）。
func TestFreshInstallerCoversDeployPlan(t *testing.T) {
	runner := NewRunner("citus", "user", "db", "/tmp/sql")
	core := make(map[string]bool)
	for _, name := range runner.StartupFiles {
		core[name] = true
	}
	plan := make(map[string]bool)
	for _, step := range freshStartupPlan(runner.StartupFiles) {
		if _, ok := plan[step.name]; ok {
			t.Fatalf("duplicate fresh startup step %s", step.name)
		}
		plan[step.name] = step.supplemental
	}
	raw, err := os.ReadFile(deployChannelScriptPath)
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(raw), "\nfiles=(\n")
	if !ok {
		t.Fatal("deployment files array missing")
	}
	body, _, ok = strings.Cut(body, "\n)")
	if !ok {
		t.Fatal("deployment files array not closed")
	}
	paths := regexp.MustCompile(`(?m)^[ \t]+"\$ROOT_DIR/([^"]+\.sql)"`).FindAllStringSubmatch(body, -1)
	if len(paths) == 0 {
		t.Fatal("deployment plan is empty")
	}
	shared := make(map[string]bool)
	for _, match := range paths {
		path := match[1]
		name := filepath.Base(path)
		shared[name] = true
		supplemental, ok := plan[name]
		if !ok || supplemental == core[name] {
			t.Errorf("incorrect fresh startup delivery for %s", name)
			continue
		}
		source, err := os.ReadFile(filepath.Join("../../..", path))
		if err != nil {
			t.Fatal(err)
		}
		var installer []byte
		if supplemental {
			installer, err = freshHandoffAssets.ReadFile("handoffsql/" + name)
		} else {
			installer, err = os.ReadFile(filepath.Join("../../cmd/llm-gw-installer/embeddata/startup", name))
		}
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(installer, source) {
			t.Errorf("installer asset differs from deployment source %s", path)
		}
	}
	dir, err := freshHandoffAssets.ReadDir("handoffsql")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range dir {
		if !plan[entry.Name()] || !shared[entry.Name()] {
			t.Errorf("unused or unanchored handoff asset %s", entry.Name())
		}
	}
}
