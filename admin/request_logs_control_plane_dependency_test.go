// request_logs_control_plane_dependency_test.go — 2026-10-02。
//
// §9.9：**本表与 stopWriteClassification 是两条正交的轴，不是同一张表的第六档。**
//
// 已有那张表（request_logs_stop_write_classification_test.go）的五档
// ——errors_out / silently_empty / silently_frozen / unaffected /
// validator_dual_read——描述的都是**「读点的输出长什么样」**：接口返回 200 还是
// 500、结果集空不空、冻不冻结。所以它们全都落在「响应面」。
//
// 但 S4 停写还有一类读点，它���输出既不是 200 也不是 500，而是**去改数据库里的
// 另一个状态**：会话 id、轮次序号、凭据可用性、探针节奏。这一类在旧表里**没有
// 对应格**——不是判错了，是那张表的坐标系里没有这个维度。
//
// 为什么这不是学术问题：停写的门控（storage.request_logs_write_enabled）只管
// **写侧**。一个**读** v1 去决定「要不要写/写什么」的读点，永远落在门控之外。
// 于是 S4 可以在「所有读端分级都合格」的情况下，仍然悄悄改变写入行为。
//
// 最重的一条是 bg/credential_recovery.go：lookbackCandidateSQL 的查询结果直接
// 授权一次 URSM v2 恢复写入（success=true），代码注释原文写着
// 「success=true is justified by the SQL predicate」。停写后这个前提被冻结
// 36 小时，然后永久消失——**失效方向是放行写入授权**，与 2026-10-02 修掉的
// s4_ready 真空为绿同一族，只是这次放行的是凭据恢复，不是切流。
package admin

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// controlPlaneVerdict 是「这个 v1 读点的输出**决定**了什么」。
type controlPlaneVerdict struct {
	// Feeds：读点结果被喂给哪个决策（人话，必须能独立看懂）。
	Feeds string

	// Live：是否存在**活着的**消费方。
	// 这一维不可省：本表第一条登记（popularity_tracker）在核实后被判为
	// dormant——它默认关闭，且它唯一的输出 GetRecommendedProbeInterval
	// 全仓没有生产调用方。把它当成活的会造出一个不存在的风险。
	Live bool

	// Gated：该消费方是否被 S4 写门覆盖。false = 门管不到它。
	Gated bool

	// Evidence：必须在该文件的非 Go 注释代码中逐字存在的片段。
	Evidence string

	// EvidenceIn：Evidence 实际所在文件，默认为登记键本身。
	//
	// 为什么需要它（§9.49）：「消费方在 A 文件、证明读 v1 的 SQL 在 B 文件」
	// 是 §9.43/§9.44 把 SQL 抽成纯函数后的常态。两种错误做法都不可接受：
	// 把 Evidence 改成 A 文件里**没有**的文本（登记在说谎），或加一条注释让
	// 扫描器看见（伪造测量）。指路是唯一诚实的选项。
	EvidenceIn string

	// BlastRadius：Live && !Gated 时必填——「它授权/改变的具体写入是什么」。
	// 逼着每条活的、未被门控的读点都写清楚它能动什么，而不是只贴个 SQL。
	BlastRadius string

	Note string
}

const (
	// cpUnevaluated：还没查。**这是当前真实状态，不是占位符。**
	cpUnevaluated = "unevaluated"

	// cpNotControlPlane：读过 v1，但输出只流向展示/导出/对账，不决定写。
	cpNotControlPlane = "not_control_plane"

	// cpControlPlaneLive：输出决定写入或身份，且消费方是活的。
	cpControlPlaneLive = "control_plane_live"

	// cpControlPlaneDormant：结构上是控制面，但消费方默认关闭或无调用方。
	// 登记它是为了防止日后有人把它接上——那时这条会立刻变成 live。
	cpControlPlaneDormant = "control_plane_dormant"
)

var controlPlaneVerdicts = map[string]struct{ note string }{
	cpUnevaluated:         {"尚未查：不能与「已确认不是控制面」区分"},
	cpNotControlPlane:     {"只流向展示/导出/对账，不决定任何写入或身份"},
	cpControlPlaneLive:    {"活的：输出决定写入或身份"},
	cpControlPlaneDormant: {"结构上是控制面，但消费方默认关闭或无调用方"},
}

// requestLogsControlPlaneReaders 覆盖 requestLogsReadInventory 中**所有非 admin/**
// 的文件——控制面依赖只可能出现在请求路径与后台 worker 侧；admin/ 是读端展示面，
// 那条轴由 requestLogsStopWriteClassification 负责。
//
// 键必须与 requestLogsReadInventory 的非 admin 子集完全一致（由门双向核）。
var requestLogsControlPlaneReaders = map[string]controlPlaneVerdict{
	// ★ 2026-10-20（迁移 890，v1 族真删）：本表**已清空**。
	//
	// 这张表登记的是「读 v1 的读点，其输出决定了哪个写入或哪个身份」——
	// 即**控制面依赖**。而 890 之后 v1 族被物理删除 ⇒
	// **没有任何读点读 v1** ⇒ 不存在「依赖 v1 的控制面」。
	//
	// ⇒ 本表保留（不删）：它守的是「**不许出现新的 v1 控制面依赖**」。
	//
	// ⚠ 不要在这里手填「已知的例外」—— 已退役关系的残留引用登记在
	// retiredV1ReferenceExceptions（生产缺陷台账）。
}

// controlPlaneUnreviewed 是分母里**还没查**的文件。
//
// 与 stopWriteClassification 用同一套纪律：未查必须显式登记为 cpUnevaluated，
// 「表里没有」与「已确认无关」是两件事，只有前者能被门看见。
func controlPlaneUnreviewed() []string {
	var todo []string
	for file := range requestLogsReadInventory {
		if strings.HasPrefix(file, "admin/") {
			continue // 展示面，归 stopWriteClassification 那条轴
		}
		v, ok := requestLogsControlPlaneReaders[file]
		if !ok || v.Feeds == "" {
			todo = append(todo, file)
		}
	}
	sort.Strings(todo)
	return todo
}

// TestRequestLogsControlPlaneKnownEntriesAreReal 是常跑守卫：已登记的每一条都必须
// 锚在真实代码上，且「活的 + 门控外的」必须写清楚它能改动什么。
//
// 这道门针对的是本次发现的那个坐标系漏洞：如果允许把一条活的、未被门控的读点
// 登记成「读端 silently_empty」就算交差，下一批就会照着那张表继续填，把控制面
// 依赖重新藏起来。
func TestRequestLogsControlPlaneKnownEntriesAreReal(t *testing.T) {
	root := repoRootFromCaller(t)

	// ★ 890 之后（2026-10-20）：控制面表必须为空。
	//
	// v1 族被物理删除 ⇒ 没有读点读 v1 ⇒ 不存在「依赖 v1 的控制面」。
	// ⇒ 本门从「已登记的每一条都必须锚在真实代码上」翻成
	//「**表必须为空**」，并给逐条自检逻辑配**阳性对照**（合成条目必须被判出问题）。
	if len(requestLogsControlPlaneReaders) > 0 {
		var files []string
		for f := range requestLogsControlPlaneReaders {
			files = append(files, f)
		}
		sort.Strings(files)
		t.Errorf("控制面表仍有 %d 条登记 —— 890 之后 v1 族已物理删除，不存在依赖 v1 的控制面：\n  %s",
			len(files), strings.Join(files, "\n  "))
	}

	for file, v := range requestLogsControlPlaneReaders {
		// §9.49：一个文件可以是「**消费方但不是读方**」——settle worker 读 SQL
		// 已被 §9.43/§9.44 抽进 auto_route_settle_sql.go，它自己只负责调度与落库。
		// 这时它不在读点清单里，但它的 EvidenceIn 指向的**是**已知读点，
		// 「控制面不得凭空多出文件」这条不变量依然成立。
		_, isDirectReader := requestLogsReadInventory[file]
		_, isIndirectReader := indirectRequestLogsReaders[file]
		evidenceIsKnownReader := false
		if v.EvidenceIn != "" {
			_, evidenceIsKnownReader = requestLogsReadInventory[v.EvidenceIn]
			if !evidenceIsKnownReader {
				_, evidenceIsKnownReader = indirectRequestLogsReaders[v.EvidenceIn]
			}
		}
		if !isDirectReader && !isIndirectReader && !evidenceIsKnownReader {
			t.Errorf("%s: 不在 requestLogsReadInventory / indirectRequestLogsReaders 里"+
				"（EvidenceIn=%q 也不是已知读点）——控制面表不得凭空多出文件",
				file, v.EvidenceIn)
			continue
		}
		if strings.TrimSpace(v.Feeds) == "" {
			t.Errorf("%s: Feeds 为空——控制面读点必须写明它的输出决定什么", file)
			continue
		}
		verdict := cpNotControlPlane
		switch {
		case !v.Live:
			verdict = cpControlPlaneDormant
		case !v.Gated:
			verdict = cpControlPlaneLive
		}
		if _, ok := controlPlaneVerdicts[verdict]; !ok {
			t.Errorf("%s: 未知判定 %q", file, verdict)
		}
		if strings.TrimSpace(v.Evidence) == "" {
			t.Errorf("%s: Evidence 为空", file)
			continue
		}
		evFile := file
		if v.EvidenceIn != "" {
			evFile = v.EvidenceIn
		}
		raw, err := os.ReadFile(filepath.Join(root, evFile))
		if err != nil {
			t.Errorf("%s: 读取失败 %v", file, err)
			continue
		}
		if !inventoryEvidenceInCode(string(raw), v.Evidence) {
			t.Errorf("%s: Evidence 未出现在 %s 的有效非注释 Go 代码中：\n  %q", file, evFile, v.Evidence)
		}
		// 关键约束：活的且门控外的读点，必须点名它授权/改变的写入。
		// 少了这一条，「活」就只是一个形容词。
		if v.Live && !v.Gated && strings.TrimSpace(v.BlastRadius) == "" {
			t.Errorf("%s: 判定为 %s（活 + 门控外）却没有 BlastRadius——\n"+
				"必须写清它具体授权或改变了哪个写入，否则「活」不可核。",
				file, verdict)
		}
	}

	// ★ 阳性对照：逐条自检逻辑本身必须仍然有效（表空着的时候，循环不执行）。
	//
	// ⇒ 用一条**合成登记**驱动同一个判据：它既不在读点清单里、又没有 Feeds、
	// Evidence 也为空 ⇒ 必须被判出问题。
	synthFile := "synthetic/control_plane.go"
	synth := controlPlaneVerdict{Evidence: "FROM _request_logs_hot rl"}
	problems := controlPlaneEntryProblems(t, root, synthFile, synth)
	if len(problems) == 0 {
		t.Error("一条凭空多出、且没有 Feeds / Evidence 的合成登记没有被判出问题 —— " +
			"控制面自检逻辑没有牙（表空着的时候，这是唯一还能证明它有效的手段）")
	}
}

// controlPlaneEntryProblems 是一条控制面登记的**自检**（返回问题列表，空 = 合格）。
//
// 为什么抽成函数：890 之后控制面表清空 ⇒ 逐条校验不再执行 ⇒
// 「自检逻辑有没有牙」就没人证明了。抽出来才能在合成登记上直接驱动它。
func controlPlaneEntryProblems(t *testing.T, root, file string, v controlPlaneVerdict) []string {
	t.Helper()
	var problems []string
	_, isDirectReader := requestLogsReadInventory[file]
	_, isIndirectReader := indirectRequestLogsReaders[file]
	evidenceIsKnownReader := false
	if v.EvidenceIn != "" {
		_, evidenceIsKnownReader = requestLogsReadInventory[v.EvidenceIn]
		if !evidenceIsKnownReader {
			_, evidenceIsKnownReader = indirectRequestLogsReaders[v.EvidenceIn]
		}
	}
	if !isDirectReader && !isIndirectReader && !evidenceIsKnownReader {
		problems = append(problems, "不在 requestLogsReadInventory / indirectRequestLogsReaders 里"+
			"（EvidenceIn 也不是已知读点）——控制面表不得凭空多出文件")
	}
	if strings.TrimSpace(v.Feeds) == "" {
		problems = append(problems, "Feeds 为空——控制面读点必须写明它的输出决定什么")
	}
	verdict := cpNotControlPlane
	switch {
	case !v.Live:
		verdict = cpControlPlaneDormant
	case !v.Gated:
		verdict = cpControlPlaneLive
	}
	if _, ok := controlPlaneVerdicts[verdict]; !ok {
		problems = append(problems, "未知判定 "+verdict)
	}
	if strings.TrimSpace(v.Evidence) == "" {
		problems = append(problems, "Evidence 为空")
	} else {
		evFile := file
		if v.EvidenceIn != "" {
			evFile = v.EvidenceIn
		}
		raw, err := os.ReadFile(filepath.Join(root, evFile))
		if err != nil {
			problems = append(problems, "读取失败 "+err.Error())
		} else if !inventoryEvidenceInCode(string(raw), v.Evidence) {
			problems = append(problems, "Evidence 未出现在 "+evFile+" 的有效非注释 Go 代码中")
		}
	}
	if v.Live && !v.Gated && strings.TrimSpace(v.BlastRadius) == "" {
		problems = append(problems, "判定为活 + 门控外却没有 BlastRadius")
	}
	return problems
}
