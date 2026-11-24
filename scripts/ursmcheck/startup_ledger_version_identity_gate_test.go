//go:build !integration

package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// startup_ledger_version_identity_gate_test.go —— startup 迁移的「台账自登记版本号」身份门。
//
// ── 判据 ──────────────────────────────────────────────────────────────────
//
// ★ 一条 startup 迁移写进 schema_migrations 的版本号，★ 必须**逐字等于它自己
// 文件名 "_" 之前的那一段**（★ 与安装器 migrationVersionOf 同口径：★ 取整串，
// ★ **不折叠**字母后缀 ⇒ ★ '983b' 就是 '983b'）。
//
// ★ 例外清单：scripts/ursmcheck/startup_ledger_version_exemptions.tsv
//   （★ 9 行，★ kind ∈ duty（职责内）/ skew（真错位但存量豁免））
//
// ── 为什么要有这道门（★ 2026-11-24 第四轮 L1 调查实测）─────────────────────
//
// ★ 缺陷形态：★ 有几条 startup 迁移把自己的版本号**写错一格**，★ 而每条的自登记
// 语句都是 `ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description`
// ⇒ ★★ **后一条把前一条那行的 description 覆盖掉** ⇒ ★★ 生产账上 5 行的
// description **全部属于「下一条」** ⇒ ★★ 984 那条自己的描述**从未在账上出现**。
//
// ★ ★★ 为什么**长期没有任何门发现它**（★ 本轮实测的关键）★★：
//   ★ 部署通道契约门的漂移门抽取引号字符串用 `'[a-z_][a-z0-9_]{4,}'`
//     ⇒ ★★ **要求 ≥5 字符** ⇒ ★★ 而迁移自登记的版本号是**3 位数字**
//     （'985'/'986'/'987'/'988'）⇒ ★★ **3 位版本号字面量永远不参与对拍**
//     ⇒ ★★ 「探针判据里的版本号」与「迁移自登记的版本号」不一致这件事，
//     ★★ **在结构上无法被任何现有门发现** ★★。
//   ★ ⇒ ★★ 本门直接读迁移文件自己的自登记语句，★ **绕开漂移门**，★ 补上这个洞 ★★。
//
// ── 门的形状（★ 同构 L14 的增量门：★ 存量豁免 + 新增即红）────────────────
//
// ★ R1「身份不符」：★ 自登记版本号 ≠ 文件名版本号，★ 且不在豁免表里 ⇒ **红并点名**；
// ★ ★★ **期望版本号的两段构造（★ 本轮实测订正，★ 避免盲区）★★**：
//   ★ ① 文件名匹配 `^[0-9]+[a-z]*_` ⇒ ★ 取整串（★ **不折叠**字母后缀 ⇒ ★ '983b' 就是 '983b'）；
//   ★ ② **不匹配**（★ 视野外，★ 实测只有 1 条：★ 日期命名族
//     `2026-07-13-multimodal-token-fields-hot.sql`）⇒ ★ ★★ **兜底取文件名去掉 `.sql` 的整串** ★★
//     ⇒ ★★ 它恰好等于该件自登记的版本号 ⇒ ★★ **因此也在判据视野内**（★ 不是「Skip」⇒ ★ 不是假绿）★★。
// ★ R2「豁免棘轮」：★ 豁免表行数**只许减不许增**（★ 上限 9）⇒ **红**；
// ★ R3「豁免已无用」：★ 豁免表某行对应的 (文件, 版本号) 已经不存在、或已经相等
//   ⇒ **红**（★ 提示摘掉该行，★ 基线更紧）；
// ★ R4「豁免表卫生」：★ 必须**按文件名有序且无重复** ⇒ **红**。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const startupLedgerExemptionsFile = "scripts/ursmcheck/startup_ledger_version_exemptions.tsv"

// startupLedgerExemptionCap 是豁免表行数上限（★ 2026-11-24 基线 = 9 行）。
const startupLedgerExemptionCap = 9

var (
	// startupLedgerInsertRe 抽出「写进 schema_migrations 的版本号」。
	startupLedgerInsertRe = regexp.MustCompile(
		`(?i)INSERT\s+INTO\s+(?:public\.)?schema_migrations\s*\([^)]*\)\s*VALUES\s*\(\s*'([^']+)'`)
	// startupLedgerFileVersionRe 取文件名 "_" 之前的整串（★ 不折叠字母后缀）。
	startupLedgerFileVersionRe = regexp.MustCompile(`^([0-9]+[a-z]*)_`)
)

// startupLedgerExpectedVersion 算出一条迁移**应当**自登记的版本号（★ 纯函数，★ 便于变异自证）。
// ★ 两段构造：
//
//	★ ① 文件名匹配 `^[0-9]+[a-z]*_` ⇒ ★ 取整串（★ '983b' 就是 '983b'，★ **不折叠**）；
//	★ ② 不匹配 ⇒ ★ 兜底取**文件名去掉 `.sql` 的整串**（★ 日期命名族的约定：★ 版本号 ＝ 文件名 stem）。
//
// ★ ★★ 为什么要第 ② 段：★ 否则这类件**根本不在判据视野内** ⇒ ★★ 门对它**静默放过** ⇒
// ★★★ **「不在视野内」与「通过了」不是一回事** ⇒ ★★ 静默放过就是**假绿**的一个变体 ★★★。
func startupLedgerExpectedVersion(fileName string) string {
	if m := startupLedgerFileVersionRe.FindStringSubmatch(fileName); m != nil {
		return m[1]
	}
	return strings.TrimSuffix(fileName, ".sql")
}

// startupLedgerIdentity 是门判据的**纯函数核心**（★ 便于变异自证）。
// files: 文件名 → 该文件自登记写下的版本号列表。
// expected: 文件名 → 该文件应当自登记的版本号（★ 文件名 "_" 之前的整串）。
// exempt: (文件名, 版本号) → 豁免。
// 返回未豁免且与期望不符的 (文件名, 实际值, 期望值) 列表。
func startupLedgerIdentitySkews(files map[string][]string, expected map[string]string,
	exempt map[string]bool) []string {
	var out []string
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		want, ok := expected[n]
		if !ok {
			continue // ★ 文件名不是「数字前缀[_]」形态 ⇒ 不在判据视野内
		}
		for _, v := range files[n] {
			if v == want {
				continue
			}
			if exempt[n+"\x00"+v] {
				continue
			}
			out = append(out, n+"\x00"+v+"\x00"+want)
		}
	}
	sort.Strings(out)
	return out
}

// startupLedgerCollect 扫 sql/migrations/startup 下的迁移文件。
//
// ★ ★★ 返回三样东西，★ 它们的**覆盖范围不同**（★ 本轮实测踩过的坑）★★：
//
//	★ files    ＝ **有自登记段**的文件 → 文件名 → 它自登记写下的版本号列表
//	★ expected ＝ **有自登记段**的文件 → 文件名 → 期望版本号（★ 身份门只看这些）
//	★ allByVer ＝ **全部**迁移文件（含**没有自登记段**的）→ 期望版本号 → 文件名
//	  ★ ★★ 为什么要单独一张全量反查表：★ 形态④「替没记账的件记账」要判定
//	  ★★ 「被写的那个号，★ 对应的迁移件**自己有没有自登记段**」★★
//	  ★★ 而**没有自登记段的件根本不在 files 里** ⇒ ★★ 若反查表只从 files 建 ⇒ ★★
//	  ★★ 形态④**永远判不出来** ⇒ ★★ 665→'663' 会被误判成「不是职责内」★★
//	  ★★ ⇒ ★★ **反查表必须覆盖全部迁移文件，★ 与有无自登记段无关** ★★。
func startupLedgerCollect(t *testing.T, root string) (map[string][]string, map[string]string, map[string]string, int) {
	t.Helper()
	dir := filepath.Join(root, "sql", "migrations", "startup")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("★ 读 %s 失败：%v", dir, err)
	}
	files := map[string][]string{}
	expected := map[string]string{}
	allByVer := map[string]string{}
	scanned := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") || strings.HasSuffix(e.Name(), ".down.sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("★ 读 %s 失败：%v", e.Name(), err)
		}
		scanned++
		// ★ ★★ 全量反查表：★ **每**一件顶层迁移都登记（★ 不看它有没有自登记段）★★。
		allByVer[startupLedgerExpectedVersion(e.Name())] = e.Name()
		vs := startupLedgerInsertRe.FindAllStringSubmatch(string(data), -1)
		if len(vs) == 0 {
			continue
		}
		names := make([]string, 0, len(vs))
		for _, m := range vs {
			names = append(names, m[1])
		}
		files[e.Name()] = names
		expected[e.Name()] = startupLedgerExpectedVersion(e.Name())
	}
	return files, expected, allByVer, scanned
}

// startupLedgerReadExemptions 读豁免表（★ 读不到必须硬失败 ⇒ 不许 Skip）。
func startupLedgerReadExemptions(t *testing.T, root string) ([]string, map[string]bool, map[string]string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, startupLedgerExemptionsFile))
	if err != nil {
		// ★ ★ 读不到豁免表必须**硬失败** ★ ★：★ 读不到就 Skip 是一种假绿。
		t.Fatalf("★ 读豁免表 %s 失败（★ 读不到不许 Skip，★ 那是假绿）：%v",
			startupLedgerExemptionsFile, err)
	}
	var order []string
	exempt := map[string]bool{}
	kind := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			t.Fatalf("★ 豁免表行格式不对（★ 期望 file<TAB>version<TAB>kind<TAB>reason）：%q", line)
		}
		key := parts[0] + "\x00" + parts[1]
		order = append(order, parts[0]+"\t"+parts[1])
		exempt[key] = true
		kind[key] = parts[2]
	}
	if len(order) == 0 {
		t.Fatalf("★ 豁免表为空 ⇒ ★ 判据的输入集合不对")
	}
	return order, exempt, kind
}

// startupLedgerDutyShapeOK 判定一条豁免**是不是真的属于「职责内」形态**。
//
// ★ ★★ 本函数**不许读豁免表里的 kind 列** ⇒ ★★ 否则就是自证 ⇒ ★★ 那正是变异臂 D 暴露的盲区 ★★。
// ★ 它只依据【文件名 + 该件实际自登记的版本号列表】⇒ ★ **与表里写的标签相互独立** ⇒ ★ 可交叉。
//
// ★ ★★ 已知职责内形态（★ 四类，★ 全部由【迁移集合自身】的结构事实判定）★★：
//
//	★ ① **代他人补记账**：★ 该件写了**两个**版本号、★ 其中一个等于该件自己的期望版本号
//	  （★ 即：★ 它自己的号写对了，★ 另一个号是**替别人补的**）★
//	  ⇒ 实测：★ 681 写 '665'（★ 替 665 补）＋ 自己的 '681'（★ 写对了）★★
//	★ ② **台账回填迁移**：★ 文件名含 `ledger_backfill` ⇒ ★ 职责就是往某个旧号补记
//	  ⇒ 实测：★ 923 写 '900'（★ 它的文件头写明「把 900~920 这 20 条迁移补登记」）★★
//	★ ③ **非编号文本**：★ 该件自登记的版本号**不是纯数字**（★ 如 445 的日期文本）★★
//	★ ④ **替没记账的件记账**：★ 写的是**另一件真实存在的迁移的号**，★ 而那件迁移**自己没有自登记段**
//	  ⇒ 实测：★ 665 写 '663'（★ 而 663_training_export.sql **自己没有自登记段**）★★
//	★ ⑤ **批量补记**：★ 该件一次自登记**多个**（★ ≥2）**别人的**号、★ 且**不登记自己**
//	  ⇒ 实测：★ 923 写 '900'~'920'（★ 共 20 行、★ 缺 '918'、★ 且**自己不登记 '923'**）★★
func startupLedgerDutyShapeOK(file, ver, want string, actual []string) bool {
	// ★ 形态 ③：自登记值不是纯数字 ⇒ 职责内（★ 非编号文本族）。
	if !startupLedgerNumericVersionRe.MatchString(ver) {
		return true
	}
	// ★ 形态 ①：代他人补记账（★ 该件写了多个版本号、★ 且**自己那个是对的**）。
	if len(actual) > 1 {
		for _, v := range actual {
			if v == want {
				return true
			}
		}
	}
	// ★ ★★ 形态 ⑤：批量补记（★ 一次登记多个**别人的**号、★ 且**自己不登记**）★★。
	// ★ ★★ 这条必须放在形态①**之前**：★ 否则 923（★ 写 20 个别人的号、★ 自己不登记）
	// ★★ 会被形态①误判成「有自己那个对的号」⇒ ★★ 判据必须按**真实形态**排序 ★★。
	if len(actual) >= 2 && !startupLedgerContains(actual, want) {
		return true
	}
	// ★ 形态 ②：台账回填迁移。
	if strings.Contains(file, "ledger_backfill") {
		return true
	}
	// ★ 形态 ④：写的是「另一件**没有自登记段**的迁移」的号 ⇒ ★ 替没记账的件记账。
	if other, ok := startupLedgerFileByExpectedVersion[ver]; ok && other != file {
		if _, hasLedger := startupLedgerAllSelfRegistered[other]; !hasLedger {
			return true
		}
	}
	return false
}

// startupLedgerContains 判「列表里有没有这个值」。
func startupLedgerContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

var (
	// startupLedgerNumericVersionRe 判「纯数字版本号」。
	startupLedgerNumericVersionRe = regexp.MustCompile(`^[0-9]+$`)
	// startupLedgerAllSelfRegistered 是全量迁移的自登记快照（★ 供职责内形态判定用）。
	// ★ ★★ 它由 startupLedgerCollect 在扫完全部文件后填充 ⇒ ★★ 因此**与表内容无关** ⇒ ★★ 不自证 ★★。
	startupLedgerAllSelfRegistered = map[string][]string{}
	// startupLedgerFileByExpectedVersion 是「期望版本号 → 文件名」的反查表（★ 同上，★ 与表内容无关）。
	startupLedgerFileByExpectedVersion = map[string]string{}
)

// ── 主门 ──────────────────────────────────────────────────────────────────

func TestStartupLedgerVersionIdentityGate(t *testing.T) {
	root := envRepoRoot(t)
	files, expected, allByVer, scanned := startupLedgerCollect(t, root)
	// ★ ★★ 填充全量自登记快照（★ 供职责内形态判定用；★ 与豁免表内容**无关** ⇒ 不自证）★★。
	startupLedgerAllSelfRegistered = files
	startupLedgerFileByExpectedVersion = allByVer
	order, exempt, kind := startupLedgerReadExemptions(t, root)

	// ★ R1：身份不符且未豁免 ⇒ 红并点名。
	skews := startupLedgerIdentitySkews(files, expected, exempt)
	if len(skews) > 0 {
		var lines []string
		for _, s := range skews {
			p := strings.Split(s, "\x00")
			lines = append(lines, "    "+p[0]+"  自登记 '"+p[1]+"'  应为 '"+p[2]+"'")
		}
		t.Errorf("★★ 发现 %d 处「startup 迁移的自登记版本号 ≠ 文件名版本号」且**不在豁免表里** ⇒ ★★ "+
			"★ 每条自登记都是 ON CONFLICT (version) DO UPDATE ⇒ ★★ **后一条会覆盖前一条那行的 description** ⇒ ★★ "+
			"★ 新增这样的迁移必须立刻改正（★ 存量错位见 %s 的 kind=skew 行）★★\n%s",
			len(skews), startupLedgerExemptionsFile, strings.Join(lines, "\n"))
	}

	// ★ R2：豁免表行数只许减不许增。
	if len(order) > startupLedgerExemptionCap {
		t.Errorf("★★ 豁免表有 %d 行，★ 超过上限 %d 行 ⇒ ★★ 豁免只许减不许增 ★★\n"+
			"★★ （★ 真正的错位要**改迁移字节让它归位**，★ 不是往豁免表里加一行）★★",
			len(order), startupLedgerExemptionCap)
	}

	// ★ R3：豁免行若已不再需要 ⇒ 红（★ 提示摘掉，★ 基线更紧）。
	for _, k := range order {
		p := strings.Split(k, "\t")
		file, ver := p[0], p[1]
		actual, stillThere := files[file]
		if !stillThere {
			t.Errorf("★★ 豁免表里的 %s（版本 '%s'）对应的**文件已不存在** ⇒ ★★ 请摘掉这一行 ★★",
				file, ver)
			continue
		}
		stillMismatch := false
		for _, v := range actual {
			if v == ver {
				stillMismatch = true
			}
		}
		if !stillMismatch {
			t.Errorf("★★ 豁免表里的 %s（版本 '%s'）**已不再自登记该版本号** ⇒ ★★ 请摘掉这一行 ★★\n"+
				"★★ （★ 该迁移可能已被改成正确的版本号 ⇒ ★ 豁免就该收掉）★★", file, ver)
			continue
		}
		if want, ok := expected[file]; ok && ver == want {
			t.Errorf("★★ 豁免表里的 %s（版本 '%s'）**已经等于文件名版本号** ⇒ ★★ 请摘掉这一行 ★★",
				file, ver)
		}
	}

	// ★ R4：豁免表卫生（按文件名有序 ＋ 无重复）。
	if !sort.StringsAreSorted(order) {
		t.Errorf("★★ 豁免表**不是按文件名有序的** ⇒ ★ 请按文件名升序排列（★ %s）★★",
			startupLedgerExemptionsFile)
	}
	seen := map[string]bool{}
	for _, k := range order {
		if seen[k] {
			t.Errorf("★★ 豁免表里有**重复**条目 %s ⇒ ★ 请去重（★ %s）★★",
				strings.ReplaceAll(k, "\t", " '"), startupLedgerExemptionsFile)
		}
		seen[k] = true
	}
	for k, kd := range kind {
		if kd != "duty" && kd != "skew" {
			t.Errorf("★★ 豁免表里 %s 的 kind='%s' ⇒ ★★ 只允许 'duty' 或 'skew' ★★",
				strings.ReplaceAll(k, "\x00", " '"), kd)
		}
	}

	// ★ ★★ R5「kind 交叉约束」（★ 本轮实测补上的第二个盲区）★★：
	// ★ ★★ 为什么要有这条：★ `kind` 列是**表内自述**，★ 若无外部约束 ⇒ ★★ **真错位可以被
	// ★★ 悄悄改写成 kind='duty' 而门毫无反应** ⇒ ★★ **而这正是 L1 类缺陷复发的路径** ⇒ ★★
	// ★★ 实测（★ 变异臂 D）：★ 把 985 那行 'skew' 改成 'duty' ⇒ ★★ **门不红** ★★。
	// ★ ⇒ 本条把 kind 与【文件的真实形态】交叉起来（★ **两个方向都要堵**）：★
	//   ★ kind='skew' ⇒ ★ 该行的版本号 **必须** ≠ 该文件名的期望版本号（★ 否则它其实已归位）★
	//     ★ ★★ **且必须不落在任何「职责内形态」里**（★ 否则它其实**是**职责内 ⇒ ★★ 标成 skew 是**误导**）★★；
	//   ★ kind='duty' ⇒ ★ 该行必须落在**已知职责内形态**里（★ 见下表），★ 否则红 ⇒ ★★
	//   ★★ **「把真错位降级成 duty」与「把职责内升级成 skew」两条伪装路径都被堵死** ★★。
	for _, k := range order {
		p := strings.Split(k, "\t")
		file, ver := p[0], p[1]
		kd := kind[file+"\x00"+ver]
		want := expected[file]
		isDutyShape := startupLedgerDutyShapeOK(file, ver, want, files[file])
		switch kd {
		case "skew":
			if want != "" && ver == want {
				t.Errorf("★★ 豁免表里 %s（版本 '%s'）标成 kind='skew'，★ ★ 但它的版本号**已经等于**期望版本号 '%s' ★★\n"+
					"★★ （★ 它其实已归位 ⇒ ★ kind 应为 'duty' 或**直接摘掉这一行**；★★ 留着 skew 是**误导性标注**）★★",
					file, ver, want)
			}
			if isDutyShape {
				t.Errorf("★★ 豁免表里 %s（版本 '%s'）标成 kind='skew'，★ ★ 但它**实际落在职责内形态**里 ★★\n"+
					"★★ （★ 职责内的判据见下方 duty 提示；★★ **标成 skew 是误导性标注** ⇒ ★ 请改回 'duty'）★★",
					file, ver)
			}
		case "duty":
			if !isDutyShape {
				t.Errorf("★★ 豁免表里 %s（版本 '%s'）标成 kind='duty'，★ ★ 但它**不在已知职责内形态**里 ★★\n"+
					"★★ （★ 职责内只有五种形态：★ ① **代他人补记账**（★ 该件写了两个号、★ 自己那个是对的）★\n"+
					"★★ 　　　　　　 ② **批量补记**（★ 一次登记多个别人的号、★ 且自己不登记）★\n"+
					"★★ 　　　　　　 ③ **台账回填**（★ 文件名含 ledger_backfill）★\n"+
					"★★ 　　　　　　 ④ **非编号文本**（★ 445→日期文本）★\n"+
					"★★ 　　　　　　 ⑤ **替没记账的件记账**（★ 665→'663'，★ 而 663 那件没有自登记段）★\n"+
					"★★ 　　　　　　 ★ **若是真错位却标成 duty ⇒ 请改回 'skew'，★ 不要用 duty 掩盖它**）★★",
					file, ver)
			}
		}
	}

	withLedger, equal := 0, 0
	for f, vs := range files {
		withLedger++
		want, ok := expected[f]
		if !ok {
			continue
		}
		for _, v := range vs {
			if v == want {
				equal++
				break
			}
		}
	}
	t.Logf("★ 台账版本身份门：扫 %d 个 startup 迁移、%d 条有自登记段；"+
		"文件名版本号与自登记逐字相等 %d 条；豁免 %d 行（★ 上限 %d）★",
		scanned, withLedger, equal, len(order), startupLedgerExemptionCap)
}

// ── 变异自证（★ 门必须真的能红）────────────────────────────────────────

func TestStartupLedgerVersionIdentityGateBites(t *testing.T) {
	files := map[string][]string{
		"985_ok.sql":     {"985"},
		"986_skewed.sql": {"985"}, // ★ 写错一格
	}
	expected := map[string]string{
		"985_ok.sql":     "985",
		"986_skewed.sql": "986",
	}
	exempt := map[string]bool{}

	skews := startupLedgerIdentitySkews(files, expected, exempt)
	if len(skews) != 1 {
		t.Fatalf("★ 变异自证失败：★ 期望恰好 1 处 skew，★ 实得 %d（%v）", len(skews), skews)
	}
	if !strings.Contains(skews[0], "986_skewed.sql") {
		t.Fatalf("★ 变异自证失败：★ 期望点名 986_skewed.sql，★ 实得 %q", skews[0])
	}

	// ★ 变异臂 2：★ 加进豁免 ⇒ 必须不再报 ⇒ ★ 证明豁免路径是通的（★ 不是无条件放过）。
	exempt["986_skewed.sql\x00985"] = true
	if got := startupLedgerIdentitySkews(files, expected, exempt); len(got) != 0 {
		t.Fatalf("★ 变异自证失败：★ 豁免后不该再报，★ 实得 %v", got)
	}

	// ★ 变异臂 3：★ 字母后缀必须逐字匹配（★ '983b' 不折叠成 '983'）。
	f2 := map[string][]string{"983b_x.sql": {"983b"}}
	e2 := map[string]string{"983b_x.sql": "983b"}
	if got := startupLedgerIdentitySkews(f2, e2, exempt); len(got) != 0 {
		t.Fatalf("★ 变异自证失败：★ '983b' 应逐字相等，★ 实得 %v", got)
	}

	// ★ ★★ 变异臂 4：★ 期望版本号的两段构造（★ 日期命名族必须**在视野内**，★ 不能被静默放过）★★。
	cases := []struct {
		name string
		want string
	}{
		{"925_supplier_contract_rates.sql", "925"},
		{"983b_add_raw_model_name.sql", "983b"}, // ★ 字母后缀不折叠
		{"983c_add_occurred_cursor_index.sql", "983c"},
		{"999a_data_maintenance_playbook.sql", "999a"},
		{"2026-07-13-multimodal-token-fields-hot.sql", "2026-07-13-multimodal-token-fields-hot"}, // ★ 兜底＝文件名 stem
		{"984_session_universe_cost_aware_probe_scope.sql", "984"},
	}
	for _, c := range cases {
		if got := startupLedgerExpectedVersion(c.name); got != c.want {
			t.Fatalf("★ 变异自证失败：★ %s 的期望版本号应为 '%s'，★ 实得 '%s'",
				c.name, c.want, got)
		}
	}

	// ★ ★★ 变异臂 5：★ 日期命名件若自登记**错**了 ⇒ 必须被抓出来（★ 不能因「不在视野内」而假绿）★★。
	f3 := map[string][]string{"2026-07-20-foo.sql": {"2026-07-19-foo"}}
	e3 := map[string]string{"2026-07-20-foo.sql": "2026-07-20-foo"}
	got3 := startupLedgerIdentitySkews(f3, e3, map[string]bool{})
	if len(got3) != 1 {
		t.Fatalf("★ 变异自证失败：★ 日期命名件写错版本号必须被抓出，★ 实得 %v", got3)
	}
}
