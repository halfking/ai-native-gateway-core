package db

import (
	"strings"
	"testing"
)

// 裁决表的承重验证（2026-10-02，审计 §9.22 决策 2）。
//
// 这道门守的不是「表里有没有条目」而是**表与契约是否还对得上**：34 条 NULL
// 补位里真正需要人工裁决的只有 6 条，其余 28 条早已被 734 的 details JOIN
// 换成真实特征列（§9.21 误以为 32 条待裁，判据取自 710 形态而非现网 734 体）。
// 门的价值在往后的每一次增列：新增一条补位而没写裁决，当场红。

// TestEveryPaddedSessionColumnHasAVerdict 是本文件的主门。
func TestEveryPaddedSessionColumnHasAVerdict(t *testing.T) {
	padded := paddedSessionColumns()
	if len(padded) == 0 {
		t.Fatal("会话投影里一条 NULL 补位都没有——要么投影写法变了，要么解析器坏了。" +
			"在没弄清之前不要相信下面的「全部已裁决」")
	}
	for _, name := range padded {
		entry, ok := paddedColumnVerdicts[name]
		if !ok {
			t.Errorf("会话投影的 NULL 补位列 %q 没有裁决条目。\n"+
				"补位对读方是静默的（session 分臂拿 NULL、接口照样 200），所以每条补位都"+
				"必须先回答「session 侧有没有同一个东西的列」：有就投影、语义不同就保持"+
				"NULL 并写明差异、没有这个事实就登记 no-session-source、无读无写就随 v1 退役。"+
				"请在 db/request_logs_view_padded_columns.go 补条目，evidence 必填。",
				name)
			continue
		}
		assertVerdictWellFormed(t, name, entry)
	}
	// 反向：表里有的条目必须真的在补位集合里。表里多一条 = 有人把裁决写了却
	// 忘了改投影（或反过来），两种都是「文档与代码各说各话」。
	for name := range paddedColumnVerdicts {
		found := false
		for _, p := range padded {
			if p == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("paddedColumnVerdicts 里的 %q 已不是会话投影的 NULL 补位列 —— "+
				"要么它已被投影/改名（请把它挪进 registeredProjectionAppends 并删掉这条裁决），"+
				"要么它已经不在契约里（请删掉这条裁决）", name)
		}
	}
}

// TestRejectedProjectionsAreStillRejected 守 rejectedProjections 的反向：
// 哪天有人把这些列投影进来了，登记必须同时被改掉，否则它会变成一份
// 「我们没投它」的假声明，而代码里它就在契约里。
func TestRejectedProjectionsAreStillRejected(t *testing.T) {
	if len(rejectedProjections) == 0 {
		t.Fatal("rejectedProjections 为空：至少 trace_events 应当留在里面（§9.22 实测依据）")
	}
	contract := map[string]bool{}
	for _, n := range canonicalColumnOrderV2 {
		contract[n] = true
	}
	for name, entry := range rejectedProjections {
		if contract[name] {
			t.Errorf("rejectedProjections 声明 %q 不投影，但它已在 canonicalColumnOrderV2 里。"+
				"要么删掉这条登记，要么把真实理由改写（投影它需要先让镜像写该列——"+
				"trace_events 在 1d/7d/30 天窗口非空率恒为 0，投影即净数据损失）", name)
		}
		assertVerdictWellFormed(t, name, entry)
	}
	// 反向：补位集合与拒绝集合不得有交集——同一列既"保持 NULL"又"不投影"
	//	是两种不同的说法，同时成立说明有人没想清楚。
	for name := range rejectedProjections {
		for _, p := range paddedSessionColumns() {
			if p == name {
				t.Errorf("%q 同时出现在 NULL 补位集合和 rejectedProjections 里："+
					"「契约内保持 NULL 补位」与「不投影」是两种不同的处置", name)
			}
		}
	}
}

// TestVerdictSetIsClosed 挡住「随手发明一个理由」。每加一个取值都要说明它与
// 既有取值的区别，否则这张表会退化成自由文本，三个月后没人能判断两个理由
// 是不是同一件事。
func TestVerdictSetIsClosed(t *testing.T) {
	known := map[paddedColumnVerdict]bool{
		verdictSameThingNoSource: true,
		verdictDifferentThing:    true,
		verdictNoSessionSource:   true,
		verdictRetireWithV1:      true,
	}
	for name, entry := range paddedColumnVerdicts {
		if !known[entry.verdict] {
			t.Errorf("%q 的裁决 %q 不在闭集内。闭集取值：%s", name, entry.verdict,
				"same-thing-not-projected / same-name-different-thing / no-session-source / retire-with-v1")
		}
	}
}

func assertVerdictWellFormed(t *testing.T, name string, entry paddedColumn) {
	t.Helper()
	if strings.TrimSpace(entry.evidence) == "" {
		t.Errorf("%q 的裁决 %q 没有 evidence。空证据的裁决等于没有裁决——"+
			"三个月后没人能判断它是过期了还是仍然成立。", name, entry.verdict)
	}
	if len(entry.evidence) < 40 {
		t.Errorf("%q 的 evidence 只有 %d 个字符，短到不像实测记录。"+
			"请写清口径与数字（真库哪张表、多少行、什么比较），而不是结论。",
			name, len(entry.evidence))
	}
}

// TestPaddedVerdictGatesAreNotVacuous 把「门本身不是空转」做成**结构性核验**，
// 而不是靠「我跑过 N 次变异」这种需要重跑才能复核的证据（2026-10-02 补）。
//
// 背景：这套门要防的是「新增一条 NULL 补位却没人裁决」。如果补位集合本身算空了、
// 裁决表被清空、或每条理由都短到等于没写，那么**上面三道门全部会绿**——
// 它们在一个空输入上平凡成立。这类门最危险的地方在于它给出的绿是真的绿，
// 但什么都没测。
//
// 所以这里逐条核「输入非空 + 理由有下限 + 双向对齐」，任何一条退化都直接红。
func TestPaddedVerdictGatesAreNotVacuous(t *testing.T) {
	// 1) 补位集合必须非空，且与 710 形态的清单**不同**——若两者相同，说明
	//    paddedSessionColumns 又退回了 710 的 30 列（§9.28.1 的头号失效形态）。
	padded := paddedSessionColumns()
	if len(padded) == 0 {
		t.Fatal("补位集合算空了：三道裁决门会在空输入上平凡通过。")
	}
	if len(padded) == legacyPaddedColumnsForDriftCheck {
		t.Errorf("补位集合(%d 列)与 710 形态清单(%d 列)同样大小 —— "+
			"很可能退回了 710 的那 30 列。§9.28.1 的恒等式是 6 = 4 + 2，现网 6 列。",
			len(padded), legacyPaddedColumnsForDriftCheck)
	}
	// 2) 裁决表必须覆盖**每一个**补位列（与主门同判据，但这里独立再算一次，
	//    免得主门和这张表共享同一个空输入而互相掩护）。
	for _, name := range padded {
		if _, ok := paddedColumnVerdicts[name]; !ok {
			t.Errorf("补位列 %q 无裁决条目（结构性核验，与主门独立）", name)
		}
	}
	// 3) 裁决表非空 + 每条理由有实质下限（不是「见上」「同左」这类占位）。
	if len(paddedColumnVerdicts) == 0 {
		t.Fatal("裁决表空了：任何新增补位都不会被要求裁决。")
	}
	for name, entry := range paddedColumnVerdicts {
		assertVerdictWellFormed(t, name, entry)
	}
	// 4) 「不投影」清单必须非空且与补位表**不相交**——后者是「两种处置同时成立」，
	//    前者是「否决记录被清空，半年后有人顺手补齐」。
	if len(rejectedProjections) == 0 {
		t.Fatal("rejectedProjections 空了：trace_events 的否决理由会随这张表一起消失。")
	}
	for name := range rejectedProjections {
		if _, inPadded := paddedColumnVerdicts[name]; inPadded {
			t.Errorf("%q 同时在裁决表与 rejectedProjections 里", name)
		}
	}
	// 5) 至少要有一个「同名不同物」的裁决：这是本项目最贵的两类不作为
	//    （id / client_ip）。如果它们被改成「有源可投影」，登记表必须跟着变，
	//    否则这张表就在替一个已被推翻的结论背书。
	different := 0
	for _, e := range paddedColumnVerdicts {
		if e.verdict == verdictDifferentThing {
			different++
		}
	}
	if different == 0 {
		t.Error("裁决表里没有任何 verdictDifferentThing 条目。id 与 client_ip " +
			"是本项目仅有的两个「session 侧有同名列但不是同一个东西」——" +
			"若它们已被推翻，请连同实测依据一起改这张表，不要让它们静默消失。")
	}
	t.Logf("门非空转核验：补位 %d 列（%v）、裁决 %d 条、否决投影 %d 条、同名不同物 %d 条",
		len(padded), padded, len(paddedColumnVerdicts), len(rejectedProjections), different)
}

// legacyPaddedColumnsForDriftCheck 是 710 形态补位集合的**规模锚**（30 列）。
// 只用规模、不用列名：列名清单在 admin 侧（legacySessionArmNullPaddedColumns710），
// 这里只防「补位集合退回 710 规模」这一个具体退化。
const legacyPaddedColumnsForDriftCheck = 30
