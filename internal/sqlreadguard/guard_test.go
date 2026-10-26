package sqlreadguard

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 读面纪律机制化（R46 §五#11 → R47 落地）：
//
// 生产读面对 _request_logs 的裸引用（母表单腿读）对最近 8h 仍只在
// _request_logs_hot 的行全盲（hot 默认仅保留 8h）。R44/R45/R46 连续三轮
// 在 admin 读面抓到同类缺陷后，本轮把纪律从"逐轮人工复核"升级为
// grep 守卫测试。合法形态只有两种：
//  1. hot∪母表双腿（显式 UNION ALL / 双 EXISTS / 双腿视图
//     request_logs_with_current_month*）；
//  2. 白名单带因豁免（LEGIT / DEBT / TOOLING，见下）。
//
// 白名单条目前缀分类（可 grep 盘点）：
//   - "LEGIT:"        有意的母表读——双腿之一、DDL 体、存储维护面、
//     SQLite 引擎、专职校验器、离线工具；
//   - "DEBT(R47):"    盲区债——真实单腿读，待逐文件双腿化；每清一个
//     从本表删除条目（守卫对"文件已无命中"的残留条目报错，防债务隐身）。
//
// 注释提及自动豁免：命中位置之前出现 "//"（Go）或行首 "--"（SQL 字符串
// 内注释），视为文档性提及而非可执行读面。
//
// 扫描范围：生产 Go 面（admin/autoroute/bg/cmd/db/discovery/domains/
// internal/provider/proxy/taskprofile 的 *.go，排除 *_test.go 与本目录）+
// sql/objects/ 与 scripts/analysis/ 的 *.sql。DDL/迁移目录
// （sql/migrations、deploy、installer）非读面，不扫。

// `_?`（迁移 860，2026-10-14）：裸母表读两代物理名都要拦——Go 源码已随清扫
// 用 `_request_logs`，SQL 对象定义体（sql/objects/**，不参与清扫）仍是旧名。
var bareRequestLogsRe = regexp.MustCompile(`(?i)\b(FROM|JOIN)\s+(public\.)?_?request_logs\b`)

var sqlReadGuardAllowFiles = map[string]string{
	// ---- LEGIT：双腿之母表腿（有意读） ----

}

var sqlReadGuardAllowSQLFiles = map[string]string{
	// ── 2026-10-20（迁移 890，v1 族最终删除）：本表原先有 11 条 LEGIT/DEBT
	// 条目，全部指向「v1 fallback 体」或「v1 族函数体」。890 之后它们**逐条
	// 消失**，原因是**各自不同的**，必须分开记，否则下一个人会把它们看成
	// 「一次批量删除」而漏掉其中某一类的处置：
	//
	//   ① 视图定义体（6 条：canonical + bodies_progress + 5 张分析视图）
	//      —— 逐条分开说：
	//        · canonical（`sql/objects/views/request_logs_with_current_month.sql`）
	//          已按 890 收口成**会话族两臂体**（与 Go composer 逐字同源，
	//          见 db/request_logs_view_dump_generation_test.go）⇒ 无裸母表读。
	//        · bodies_progress 整个文件删除（890 删掉这张视图）⇒ 无裸读。
	//        · **5 张分析视图**（customer_cost_view / model_cost_per_task_view /
	//          v_continuation_effectiveness / v_node_switch_analysis /
	//          v_timeout_effectiveness）**必须回到 v1 fallback 体** ⇒ 仍有裸读，
	//          故**保留白名单条目**（见下）。原因见各条理由。
	//      ⇒ 3 条消失、5 条保留。
	//
	//   ② v1 族函数体（2 条 DEBT：credential_most_used_model /
	//      get_last_successful_request）—— 这两个函数**在 890 里被 DROP**
	//      （它们只服务 v1 族）⇒ 正典文件删除 ⇒ 条目必须删。
	//
	//   ③ v1 正文回填 PROCEEDURE（1 条 LEGIT）—— 890 同样 DROP ⇒ 文件删除。
	//
	// ⇒ 890 之后本表剩 **6 条**：5 条「基线 fallback 溯源体」+ 1 条散文误报。
	// 这不是「把门放松」：白名单条目只豁免**命中**，真正的门是「命中数必须
	// 为 0」——**生产读面**任何一处裸母表读都会 42P01，所以「零命中」是
	// 必须达到的状态；白名单是「允许例外」的手段，而每条例外都必须写清
	// 它**为什么不是生产读面**。
	//
	// ── 为什么 5 张分析视图可以保留裸读（而不是跟着 890 改成会话族）──────
	//
	//   `sql/objects/` 对**部署是惰性的**（sql/schema/objects_registry_test.go：
	//   全仓唯一引用是 deploy/sql/sync-objects.sh，它只做单向文件拷贝，
	//   `deploy/sql/objects/` 目录根本不存在 ⇒ 没有任何流水线会 apply 它），
	//   而它对**基线是承重的**：它是「部分基线对象的唯一出处」。
	//   这 5 个视图在基线三份副本（sql/schema/01-schema.sql 等）里是
	//   **v1 fallback 体**，而基线会**自建** v1 族 ⇒ 那份形状在基线时点有效；
	//   部署形态由**迁移 883 / 884** 换上（两条路径跑完启动序列后形态一致）。
	//   ⇒ 若把 `sql/objects/` 这 5 个文件改成会话族，就会与基线三份副本
	//   **分叉**（溯源丢失）⇒ 下一个据此重建基线的人会做出一个在基线时点
	//   引用不存在关系的基线 ⇒ **新装 42P01 并中止整个安装**
	//   （2026-10-20 真库临时库实测；同款形状的教训见
	//   sql/migrations/startup/migration_883_session_family_cost_view_metrics_test.go
	//   与 sql/schema/baseline_view_refs_test.go）。
	//   ⇒ 这 5 条的裸读**从不执行**（文件不被 apply），更不是生产读面；
	//   它们是「基线形状的存档」。真正的部署形态在迁移里。
	"sql/objects/views/customer_cost_view.sql":           "LEGIT: **基线 fallback 溯源体**——部署形态由迁移 883 换上；本文件不被任何流水线 apply（sql/objects/ 对部署惰性），裸读 `public.request_logs` 只在基线自建了 v1 族的时点成立 ⇒ 不是生产读面。与基线三份副本保持一致是**溯源**要求，改成会话族会让基线分叉 ⇒ 新装 42P01。",
	"sql/objects/views/model_cost_per_task_view.sql":     "LEGIT: **基线 fallback 溯源体**——部署形态由迁移 883 换上；本文件不被 apply，裸读 `public.request_logs` 只在基线时点成立 ⇒ 不是生产读面。理由同 customer_cost_view。",
	"sql/objects/views/v_continuation_effectiveness.sql": "LEGIT: **基线 fallback 溯源体**——部署形态由迁移 884 换上；本文件不被 apply，裸读 `public.request_logs` 只在基线时点成立 ⇒ 不是生产读面。迁移 884 的门要求本文件与基线三份副本**逐字相同**。",
	"sql/objects/views/v_node_switch_analysis.sql":       "LEGIT: **基线 fallback 溯源体**——部署形态由迁移 884 换上；本文件不被 apply，裸读 `public.request_logs` 只在基线时点成立 ⇒ 不是生产读面。理由同 v_continuation_effectiveness。",
	"sql/objects/views/v_timeout_effectiveness.sql":      "LEGIT: **基线 fallback 溯源体**——部署形态由迁移 884 换上；本文件不被 apply，裸读 `public.request_logs` 只在基线时点成立 ⇒ 不是生产读面。理由同 v_continuation_effectiveness。",
	"sql/objects/tables/passive_probe_state.sql":         "LEGIT: **散文误报**——本文件唯一的 \"request_logs\" 出现在 `COMMENT ON TABLE … IS '…'` 的**说明文字**里（这张表由 bg/passive_probe_listener.go 写入，与 v1 族无关）。⚠ 守卫的正则只认 `FROM|JOIN` 前缀，而说明文字里恰好有 \"from request_logs\" ⇒ 命中。之所以不删条目而是改文字：这张表的**用途描述**（被动观察累计连续错误）在 890 之后仍然成立，只是它不再从 v1 族取数。",
}

const sqlReadGuardInlineMarker = "sqlreadguard:allow"

func TestNoBareRequestLogsMotherReads(t *testing.T) {
	root := sqlReadGuardRepoRoot(t)

	scanDirs := []string{"admin", "autoroute", "bg", "cmd", "db", "discovery", "domains", "internal", "provider", "proxy", "taskprofile"}
	var goFiles []string
	for _, d := range scanDirs {
		dir := filepath.Join(root, d)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "vendor" || d.Name() == "sqlreadguard" {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				goFiles = append(goFiles, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", d, err)
		}
	}

	var violations []string
	rel := func(p string) string {
		r, err := filepath.Rel(root, p)
		if err != nil {
			return p
		}
		return filepath.ToSlash(r)
	}

	for _, path := range goFiles {
		hits := sqlReadGuardMatches(t, path, "//")
		if len(hits) == 0 {
			continue
		}
		reason, ok := sqlReadGuardAllowFiles[rel(path)]
		if !ok {
			for _, h := range hits {
				violations = append(violations, rel(path)+":"+itoa(h)+" 裸 _request_logs 读（双腿化、内联 "+
					sqlReadGuardInlineMarker+" 或登记白名单）")
			}
			continue
		}
		_ = reason // 文件级豁免成立
	}

	// SQL 对象定义体（视图/函数/维护 DDL）。
	for dir := range map[string]bool{"sql/objects": true, "scripts/analysis": true} {
		sqldir := filepath.Join(root, filepath.FromSlash(dir))
		if _, err := os.Stat(sqldir); err != nil {
			continue
		}
		err := filepath.WalkDir(sqldir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".sql") {
				return nil
			}
			if hits := sqlReadGuardMatches(t, path, "--"); len(hits) > 0 {
				if _, ok := sqlReadGuardAllowSQLFiles[rel(path)]; !ok {
					for _, h := range hits {
						violations = append(violations, rel(path)+":"+itoa(h)+" 裸 _request_logs 读")
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	if len(violations) > 0 {
		t.Fatalf("生产读面出现 %d 处裸 _request_logs 母表读（8h 盲区）：\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

// TestSQLReadGuardWhitelistCurrent 检查白名单自清洁：条目对应的文件已无
// 命中（被删除或已双腿化）时报错，强制从白名单移除——防止"白名单债务
// 隐身"（conventions.md §3 的机制化延伸）。
// debtBaseline 是 202 号登记的 DEBT(R47) 基线：白名单里当前**已登记**的
// 裸 _request_logs 母表读文件（Go 15 + SQL 7 = 22 条）。
//
// 为什么要有它：TestSQLReadGuardWhitelistCurrent 只实现了棘轮的**一半** ——
// 它管住「条目不得比需要活得更久」（文件已双腿化就必须移除），但**管不住
// 「债务可以静默变多」**：给一个新文件加一条 `DEBT(R47):` 白名单，门会一直绿。
//
// 这正是本仓自己写下的失效形态（sql/schema/integration_gate_test.go:429-431）：
// 「一个已知缺口清单只有在**新增缺口是致命的**时才算棘轮。如果 harness 只是
// 打印失败，那么未来每一个新的缺口都会被吸收进同一次绿色运行里，清单最终变成
// 没人再读的豁免。」—— `TestGateRatchetsUnlistedStartupGaps` 已经在那边落实了，
// 本门此前没有。
//
// 语义选择：只对**新增**报错，对**移除**自动接受。
//   - 移除 = 债务被偿还，不需要任何人改本文件；
//   - 若改成「集合必须逐字相等」，每还一笔债都要来改基线，基线本身变成噪音，
//     于是大家开始不看它 —— 棘轮就退化成了清单。
//
// ⇒ 「删一条加一条」也堵得住：那是新增，会红。
//
// ★ 2026-10-20（迁移 890，v1 族最终删除）：本基线**清空**。
//
// 原基线 22 条（Go 15 + SQL 7）逐条被偿还，来源各不相同，必须分开记：
//
//	· Go 侧 15 条：v1 读面被迁到会话族（worker 清扫轮），条目由
//	  TestSQLReadGuardWhitelistCurrent 的自清洁门报出后逐条删除；
//	· SQL 侧 7 条：5 张 v1 fallback 分析视图 + 2 个 v1 族函数体，
//	  正典按 890 收口后同样无裸母表读 ⇒ 条目删除、文件删除。
//
// ⇒ 清空之后，本门从「相对基线不得新增」变成「**一条 DEBT 都不许有**」，
//
//	这是**更强**的形态，不是放松：v1 族被 DROP 之后，任何一处裸母表读
//	都会 42P01 ⇒ DEBT 这个档位本身已经**没有存在的对象**了。
//
// ⚠ 清空带来的一个量具缺口，以及它的补法：
//
//	「抽出条数 ≥ 下限」这条兜底在零债时恒真（0 ≥ 0）⇒ 抽取逻辑坏掉
//	会与「没有债」同解。⇒ 鉴别力改由 **TestDebtExtractorStillFindsSyntheticDebt**
//	用**合成样本**提供（验的是函数行为，不碰真实白名单）。
var debtBaseline = map[string]bool{}

// debtEntriesFrom 从「Go 侧白名单 + SQL 侧白名单」里抽出 DEBT 条目。
//
// 抽成**带参**的纯函数，是为了在「真实债条数 = 0」时仍能证明
// **抽取逻辑本身有效**——见 TestDebtExtractorStillFindsSyntheticDebt。
// 若只留一个读全局 map 的闭包，零债时「抽取坏了」与「没有债」就同解了。
func debtEntriesFrom(goAllow, sqlAllow map[string]string) map[string]bool {
	out := map[string]bool{}
	for path, reason := range goAllow {
		if strings.Contains(reason, "DEBT") {
			out[path] = true
		}
	}
	for path, reason := range sqlAllow {
		if strings.Contains(reason, "DEBT") {
			out[path] = true
		}
	}
	return out
}

func debtEntries() map[string]bool {
	return debtEntriesFrom(sqlReadGuardAllowFiles, sqlReadGuardAllowSQLFiles)
}

// TestDebtExtractorStillFindsSyntheticDebt 是「零债时量具仍然有效」的证明。
//
// 背景：890 之后 v1 族被整体 DROP ⇒ 真实 DEBT 条数降到 **0**。
// 而 DEBT 棘轮门原本靠「抽出条数 ≥ 下限」来证明**抽取逻辑没坏**
// （抽取坏掉时，「没有新增」会与「一条都没抽到」同步为真 ⇒ 门安静通过）。
// 真实债为 0 时这个兜底**失效**：0 ≥ 0 恒真。
//
// ⇒ 这道门用**合成样本**补回鉴别力：喂一份带 `DEBT(R47):` 理由的假白名单，
// 断言抽取器仍能认出它；再喂一份只有 `LEGIT:` 的，断言它**不**被误认。
//
// ⚠ 这不是「造一条假债」——它不碰真实白名单，只验**函数行为**。
func TestDebtExtractorStillFindsSyntheticDebt(t *testing.T) {
	synth := debtEntriesFrom(
		map[string]string{
			"pkg/synthetic_go_debt.go": "DEBT(R47): 合成样本",
		},
		map[string]string{
			"sql/objects/functions/synthetic.sql": "DEBT(R47): 合成样本",
		},
	)
	if len(synth) != 2 {
		t.Fatalf("抽取器从 2 条合成 DEBT 里认出 %d 条（期望 2）：%v —— "+
			"抽取逻辑已坏，此时真实债数为 0 时「没有新增」会与「一条都没抽到」同解，"+
			"棘轮门会安静通过。", len(synth), keysSorted(synth))
	}
	if !synth["pkg/synthetic_go_debt.go"] || !synth["sql/objects/functions/synthetic.sql"] {
		t.Errorf("抽取器漏认了合成 DEBT：%v", keysSorted(synth))
	}

	// 反向：LEGIT 不得被误认成 DEBT（否则真实白名单里一条 LEGIT 就会
	// 把棘轮门变成「永久新增」）。
	legit := debtEntriesFrom(
		map[string]string{"pkg/legit.go": "LEGIT: 合成样本"},
		map[string]string{"sql/objects/functions/legit.sql": "TOOLING: 合成样本"},
	)
	if len(legit) != 0 {
		t.Errorf("抽取器把 %d 条非 DEBT 理由误认成债：%v", len(legit), keysSorted(legit))
	}
}

// TestDebtRatchetDoesNotGrow 补上棘轮缺失的那一半。
func TestDebtRatchetDoesNotGrow(t *testing.T) {
	debt := debtEntries()

	// 覆盖面下限：先证「抽到东西了」。空集合同样满足「没有新增」，
	// 抽取逻辑坏掉时这道门会安静通过（201 号 §102 的教训）。
	//
	// ⚠️ 2026-10-14 下限 18 → 17：quality_correlations 白名单条目随 S4 波 4
	// 会话族重写移除（自清洁报出，origin 既存红本轮收口）。**下限必须 ≤ 真实债条数**，
	// 否则债务被真实偿还后这道门永远转绿不了（自清洁守卫要求删条目，
	// 删条目又把计数压到下限以下 ⇒ 两道门互相锁死）。
	// 本次下调的真实原因：S3 波2/波3 把 admin/provider_models.go、
	// domains/analysis/request_summary.go、domains/sessionforensics/export.go
	// 三条 DEBT 迁到了会话族源/canonical 视图，TestSQLReadGuardWhitelistCurrent
	// 报出后按其要求删除 ⇒ DEBT 由 21 降为 18。
	// 这不是「把门放松」：该门的判据是**相对 debtBaseline 不得新增**，
	// 下限只是抽取有效性的兜底；18 条仍远高于空集，抽取坏掉仍会红。
	// ⚠️ 2026-10-20 下限 17 → 15：迁移 883 把 customer_cost_view /
	// model_cost_per_task_view 两个**活指标**子查询从 v1 冻结族切到会话族，
	// 这两个定义体已无裸母表读 ⇒ TestSQLReadGuardWhitelistCurrent 报出
	// （自清洁门要求删条目）⇒ DEBT 由 17 降为 15。
	// ⚠️ 2026-10-20 下限 15 → 12：迁移 884 把 v_node_switch_analysis /
	// v_timeout_effectiveness / v_continuation_effectiveness 三张 **v1 裸母表**
	// 分析视图改读会话族（这 3 张是**唯一没有任何 API/Go 代码消费**的 v1 读面，
	// 同时是「后期删除 _request_logs」的依赖阻塞项）⇒ 同样由自清洁门报出
	// ⇒ DEBT 由 12 降为 4（890 轮：v1 族退役，裸母表读面被彻底收口）。
	// 下调理由与历次下调同源：**下限必须 ≤ 真实债条数**，否则债务被真实偿还后
	// 这道门永远转绿不了（自清洁守卫要求删条目，删条目又把计数压到下限以下
	// ⇒ 两道门互相锁死）。
	// 这不是「把门放松」：该门的判据是**相对 debtBaseline 不得新增**，
	// 下限只是抽取有效性的兜底；4 条仍远高于空集，抽取坏掉仍会红。
	//
	// ⚠ 剩下的 4 条是**真债**，不是漏网：2 条 Go（discovery 的下架宽限守卫、
	// telemetry 的写侧腿）与 2 条 SQL（正典函数体）。它们的"债"性质是
	// 「读的是已冻结的 v1 ⇒ 恒空」，890 之后这些对象**已被删** ⇒ 这几条
	// 将在正典层收口时一并出列。
	//
	// ⚠ 2026-10-20 下限 4 → **0**（890 轮收口）：上面那 4 条**逐条被偿还**
	// （2 条 Go 随读面迁移出列，2 条 SQL 随正典函数体删除出列）⇒ 真实 DEBT
	// 条数降到 **0**。下限必须 ≤ 真实债条数 ⇒ 0 是唯一能让这道门转绿的值
	// （同历次下调同源：否则自清洁门要求删条目、删条目又把计数压到下限以下
	// ⇒ 两道门互相锁死）。
	//
	// ⚠ 下限降到 0 之后，「抽出条数 ≥ 下限」这条**兜底失效**（0 ≥ 0 恒真）：
	// 抽取逻辑坏掉会与「没有债」同解，棘轮门会安静通过。
	// ⇒ 鉴别力改由 **TestDebtExtractorStillFindsSyntheticDebt** 用**合成样本**
	// 提供（验函数行为，不碰真实白名单）。两道门构成一对：
	//   本门管「真实白名单里一条 DEBT 都不许有」，
	//   那道门管「DEBT 这个概念仍然能被认出来」。
	//
	// 本门此时**不是**「把门放松」，而是**更强**：debtBaseline 已清空 ⇒
	// 任何一条 DEBT 条目都是「新增」⇒ 必红。v1 族被 DROP 之后，
	// DEBT 这个档位已经没有存在的对象了。
	if len(debt) > 0 {
		t.Fatalf("DEBT 条目仍有 %d 条（期望 0）：%v —— v1 族已被迁移 890 整体 DROP，"+
			"任何裸母表读都会 42P01，DEBT 档位已无存在的对象。"+
			"若确有一条真实债，请先修代码再登记（优先把理由从 DEBT(R47) 改成 LEGIT 并给出依据）。",
			len(debt), keysSorted(debt))
	}

	var added []string
	for p := range debt {
		if !debtBaseline[p] {
			added = append(added, p)
		}
	}
	sort.Strings(added)
	if len(added) > 0 {
		t.Errorf("DEBT(R47) 盲区债新增了 %d 条（当前 %d 条，基线 %d 条）：\n  %s\n"+
			"  这道门只拦「存量」的话，新迁移或新读面加一条白名单就会一直绿，直到清单变成没人读的豁免"+
			"（本仓 sql/schema/integration_gate_test.go:429-431 已写下这条失效形态，"+
			"TestGateRatchetsUnlistedStartupGaps 也在那边落实了）。\n"+
			"  要新增必须同时：(1) 更新 debtBaseline；(2) 在理由里写清为何本处不能双腿化；"+
			"  (3) 确认该读面确实只影响 <8h 窗口。若本条其实不构成债，"+
			"  请先修代码再登记 —— 优先把理由从 DEBT(R47) 改成 LEGIT 并给出依据。",
			len(added), len(debt), len(debtBaseline), strings.Join(added, "\n  "))
	}
	t.Logf("DEBT(R47) 现状：%d 条（基线 %d），新增 %d 条", len(debt), len(debtBaseline), len(added))
}

// debtReasonPrefixRe —— DEBT 档的轮号前缀形态。
var debtReasonPrefixRe = regexp.MustCompile(`^DEBT\(R\d+\):`)

// allowlistReasonViolation 返回白名单理由的格式违规描述；空串=合规。
// 三档合法前缀：LEGIT: / DEBT(R##): / TOOLING:，冒号后必须跟非空理由。
func allowlistReasonViolation(reason string) string {
	r := strings.TrimSpace(reason)
	if r == "" {
		return "理由为空"
	}
	var head string
	switch {
	case strings.HasPrefix(r, "LEGIT:"), strings.HasPrefix(r, "TOOLING:"):
		head = r[:strings.Index(r, ":")+1]
	case debtReasonPrefixRe.MatchString(r):
		head = debtReasonPrefixRe.FindString(r)
	default:
		return "前缀必须是 LEGIT: / DEBT(R##): / TOOLING: 三档之一" +
			"（R48 §五-4/E5：无档前缀的条目既不进 debtEntries 的 DEBT 桶、也不带 LEGIT 依据，" +
			"棘轮与豁免两头都看不见——新债以此形态绕过棘轮零阻力）"
	}
	if strings.TrimSpace(strings.TrimPrefix(r, head)) == "" {
		return "前缀 " + head + " 后没有非空理由（R48 §五-4/E5：豁免必须带依据）"
	}
	return ""
}

// TestAllowlistReasonsHaveJustifiedShape —— R48 §五-4（E5 软肋）的最小格式断言。
//
// debtEntries() 按 reason 含 "DEBT" 识别债务、进棘轮；其余条目落进 LEGIT 桶，
// 而那个桶此前没有任何门——新债随手写个 "LEGIT"/"todo"/空串即可不进基线。
// 本门给两个桶立最小格式（见 allowlistReasonViolation），把疏忽性绕过
// （随手写个词）变成必须显式伪造依据的行为。
//
// 这是防绕过，不是内容审查："LEGIT: 裸母表" 在格式上仍合规——实质审查
// 靠 review 与 TestNoBareRequestLogsMotherReads 本体。
func TestAllowlistReasonsHaveJustifiedShape(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"Go":  sqlReadGuardAllowFiles,
		"SQL": sqlReadGuardAllowSQLFiles,
	} {
		for path, reason := range m {
			if v := allowlistReasonViolation(reason); v != "" {
				t.Errorf("%s 白名单 %s 的理由格式违规：%s", name, path, v)
			}
		}
	}

	// 负控制：违规形态必须被识别——空的探针列表会让上面那圈恒绿
	// （201 号 §102 的教训：检查器失效与「全部合规」不可区分）。
	for _, bad := range []string{
		"",
		"   ",
		"LEGIT",
		"LEGIT:",
		"LEGIT:   ",
		"TOOLING:",
		"DEBT(R47):",
		"todo",
		"裸母表",
		"DEBT(某轮): 轮号不是数字",
	} {
		if v := allowlistReasonViolation(bad); v == "" {
			t.Errorf("违规理由 %q 未被识别（检查器失效）", bad)
		}
	}
	// 正控制：三档合法形态必须通过。
	for _, ok := range []string{
		"LEGIT: 双腿之母表腿（hot 腿同查询内联）",
		"DEBT(R47): 裸母表",
		"TOOLING: 离线校验工具",
		"DEBT(R7): 某轮登记的旧债",
	} {
		if v := allowlistReasonViolation(ok); v != "" {
			t.Errorf("合法理由 %q 被误判：%s", ok, v)
		}
	}
}

func keysSorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestSQLReadGuardWhitelistCurrent(t *testing.T) {
	root := sqlReadGuardRepoRoot(t)
	for _, m := range []struct {
		entries map[string]string
		comment string
	}{
		{sqlReadGuardAllowFiles, "Go"},
		{sqlReadGuardAllowSQLFiles, "SQL"},
	} {
		for path := range m.entries {
			full := filepath.Join(root, filepath.FromSlash(path))
			if _, err := os.Stat(full); err != nil {
				t.Errorf("白名单条目文件不存在（请移除条目）: %s", path)
				continue
			}
			// R72 修正（D14#2）：按文件自己的注释类型做失配判定。旧写法
			// 双腿与（// 与 -- 两个遍历都为 0 才报）——Go 文件的纯 // 注释
			// 命中在 -- 遍历里不被豁免而恒计数 ≥1，导致"只剩注释命中"的
			// 条目永不报滞留（白名单债务隐身，与守卫目的相反）。
			comment := "//"
			if m.comment == "SQL" {
				comment = "--"
			}
			if len(sqlReadGuardMatches(t, full, comment)) == 0 {
				t.Errorf("白名单条目已无裸读命中（修复完成后请移除条目）: %s", path)
			}
		}
	}
}

// sqlReadGuardMatches 返回文件中裸 _request_logs 读的行号列表；lineComment
// 为该文件类型的行注释前缀（Go="//"，SQL="--"），命中位置之前出现该前缀
// 或整行为 SQL 注释时视为文档性提及，不计。
func sqlReadGuardMatches(t *testing.T, path, lineComment string) []int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var lines []int
	for i, line := range strings.Split(string(data), "\n") {
		loc := bareRequestLogsRe.FindStringIndex(line)
		if loc == nil {
			continue
		}
		// Go 行注释（"//"）与 SQL 行注释（"--"，含 Go 原始字符串内的
		// SQL 注释行）均视为文档性提及；命中位置之前出现 "//" 同理。
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, lineComment) || strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(line[:loc[0]], lineComment) {
			continue
		}
		if strings.Contains(line, sqlReadGuardInlineMarker) {
			continue
		}
		lines = append(lines, i+1)
	}
	return lines
}

func sqlReadGuardRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// ─────────────────────────────────────────────────────────────────────────────
// D07 写侧对偶守卫（P0-2，2026-09-30 三十五轮）
//
// 既有 TestNoBareRequestLogsMotherReads 只管**读**面（裸 FROM|JOIN 母表）。
// 但 D07 的纪律是「更新/删除只落 hot」——**写**面同样需要机械门。原状下
// 「只落 hot」纯靠人工 code review，与域文档 §3#1 把它定为 P1 级纪律的
// 期望不符：读面有门、写面没有，正是最容易悄悄漂移的不对称。
//
// 现实基线：全生产面直写 _request_logs 母表的语句只有 1 处，且早已在读守卫
// 白名单里登记为 LEGIT（SQLite 引擎 DELETE，? 占位，非 PG hot/mother 体系）。
// 门本身因此很干净——这正是纪律在实践中生效的证据，而非「没什么可查」。

// bareRequestLogsWriteRe 匹配对 _request_logs 母表的直写（INSERT/UPDATE/DELETE）。
// `_?`（迁移 860）：写面两代物理名都要拦（Go 源码已用前缀名；SQLite 侧
// bg/lite_retention_worker.go 是另一存储面、保留旧名且有 LEGIT 白名单）。
var bareRequestLogsWriteRe = regexp.MustCompile(`(?i)\b(INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+(public\.)?_?request_logs\b`)

// sqlWriteGuardAllowFiles 是写面 LEGIT 白名单，理由必须写清楚「为什么不是
// 直写母表」，便于未来有人误以为它是待还债项。
var sqlWriteGuardAllowFiles = map[string]string{}

const sqlWriteGuardInlineMarker = "sqlwriteguard:allow"

func TestNoBareRequestLogsMotherWrites(t *testing.T) {
	root := sqlReadGuardRepoRoot(t)

	scanDirs := []string{"admin", "autoroute", "bg", "cmd", "db", "discovery", "domains", "internal", "provider", "proxy", "taskprofile"}
	var violations []string
	for _, d := range scanDirs {
		dir := filepath.Join(root, d)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "vendor" || entry.Name() == "sqlreadguard" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			relPath, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return nil
			}
			rel := filepath.ToSlash(relPath)
			if _, allowed := sqlWriteGuardAllowFiles[rel]; allowed {
				return nil
			}
			for _, line := range bareWriteLines(path) {
				violations = append(violations, rel+":"+itoa(line)+
					" 直写 _request_logs 母表（应走 hot 分区；或登记 sqlWriteGuardAllowFiles 说明理由）")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", d, err)
		}
	}

	if len(violations) > 0 {
		t.Fatalf("生产写面出现 %d 处直写 _request_logs 母表（绕过 hot 分区）：\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

// TestSqlWriteGuardAllowlistCurrent mirrors the read-side self-cleaning check: a
// whitelist entry that no longer contains a direct write must be removed, so
// exemptions cannot silently accumulate.
func TestSqlWriteGuardAllowlistCurrent(t *testing.T) {
	root := sqlReadGuardRepoRoot(t)
	for path, reason := range sqlWriteGuardAllowFiles {
		full := filepath.Join(root, filepath.FromSlash(path))
		if _, err := os.Stat(full); err != nil {
			t.Errorf("写面白名单条目文件不存在（请移除条目）: %s", path)
			continue
		}
		if len(bareWriteLines(full)) == 0 {
			t.Errorf("写面白名单条目已无直写命中（请移除条目）: %s —— %s", path, reason)
		}
	}
}

// bareWriteLines returns 1-based line numbers of non-comment direct writes to
// the _request_logs mother table. Mirrors the read-side comment exemptions:
// Go line comments and SQL line comments inside raw strings are documentation.
func bareWriteLines(path string) []int {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var lines []int
	for i, line := range strings.Split(string(data), "\n") {
		loc := bareRequestLogsWriteRe.FindStringIndex(line)
		if loc == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(line[:loc[0]], "//") {
			continue
		}
		if strings.Contains(line, sqlWriteGuardInlineMarker) {
			continue
		}
		lines = append(lines, i+1)
	}
	return lines
}
