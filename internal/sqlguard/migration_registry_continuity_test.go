package sqlguard

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestMigrationRegistryStaysContinuous —— 224 号新增。
//
// 背景：`scripts/verify-migration-checksums.sh:80-82` 明写
//
//	Files present on disk but absent from the registry are reported as
//	warnings only — db-changelog.md only records recent deploys, so older
//	startup/ files are expected to be unregistered.
//
// **这个前提曾经成立，现在正在失效。** 224 号实测登记率随编号单调上升：
//
//	0–499   共 238，未登记 238（100%）
//	500–599 共  66，未登记  52（ 79%）
//	600–699 共  93，未登记  15（ 16%）
//	700–799 共  63，未登记  10（ 16%）
//	800–899 共  21，未登记   2（ 10%）   ← 近邻窗口里已有断档
//
// 而脚本 `:127` 的 `missing_in_registry unregistered` 是 **warn-only**（不 `exit 1`），
// `deploy/sql/verify-migration.sh` 也只查文件**存在**、不查登记
// ⇒ **一条忘了登记的迁移会被静默部署，且没有任何门会响。**
//
// 本门把"新迁移必须登记"从约定变成机械检查，且**只对近邻窗口断言**
// ——对全部历史文件断言会误报 321 个（那正是脚本刻意 warn-only 的原因）。
// 门要复刻脚本的**真实口径**（§168：判据的单位与宽严必须与权威规则一致）：
//   - 同一文件多行登记、任一 sha 命中即过；
//   - 只认 `\d+_[a-zA-Z0-9_]+\.sql` 的行。
const (
	// 台账位置与迁移目录（相对仓库根）
	registryPath = "../../docs/db-changelog.md"

	// 近邻窗口宽度：只看台账最大登记编号往下的这一段。
	// ⚠️ 刻意的窄口径：老文件未登记是**设计内**的（脚本明写），
	// 对它们断言会制造 321 条假阳性 —— 一道会误报的门比没有门更坏。
	windowSize = 20
)

// migrationsDirs 必须与 scripts/verify-migration-checksums.sh 的 MIG_DIRS 保持一致。
//
// ⚠️ 2026-10-06：830 被移进 manual/（手工执行，刻意不进自动序列），
// 而本门与那个脚本原本都只扫 startup/ ⇒ 台账登记的 830 在磁盘上读不到，
// 两边同时报 fatal。manual/ 里的文件是**人工**跑在生产上的那批，
// sha 校验恰恰最该保留，所以两边都必须认这个目录。
var migrationsDirs = []string{
	"../../sql/migrations/startup",
	"../../sql/migrations/manual",
}

// readMigration 按 basename 在各迁移目录里找文件并返回其内容。
// 同名存在时优先 startup/（与脚本的目录顺序一致）。
func readMigration(name string) ([]byte, error) {
	var firstErr error
	for _, d := range migrationsDirs {
		b, err := os.ReadFile(filepath.Join(d, name))
		if err == nil {
			return b, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// migFile 是迁移文件的最小信息（名字 + 编号）。
type migFile struct {
	name string
	num  int
}

// knownUnregistered 是**已登记为缺陷、尚未补登记**的迁移（224 号测得）。
//
// 与 billguard 的 knownMissing 同理：显式登记 + t.Log 打印，
// 而不是让门一直红（一个红的门会让 make guards 永久失败，
// 于是所有守卫一起失去可读性）。
// 豁免可自我失效：一旦台账补上登记行，上面的匹配成立、豁免不再被打印。
var knownUnregistered = map[string]string{
	"802_session_turn_details_gw_task_id_index.sql": "2026-10-01 合入（907d67b85），台账无登记行。",
	"820_audio_modality_backfill.sql":               "2026-10-03 并发会话合入（21e1d66cf），台账无登记行。",
	// 2026-10-06：本门自此也扫 sql/migrations/manual/（830 移进去之后），
	// 这两个 2026-07-19 的文件因此进入近邻窗口。刻意不登记的理由是**它们不是
	// schema 迁移**：前者头部自称「验证脚本」，内容是对配置/错误响应的只读
	// SELECT，用于排障定位；后者是给火山引擎提供商补 glm-5.2 模型的一次性数据
	// 订正。docs/db-changelog.md 是 schema 迁移的 sha 台账，把它们登记进去
	// 会让「有登记 = 有待部署的 schema 变更」这个含义失效。
	"20260719_add_volcano_glm52.sql": "非 schema 迁移：火山引擎 glm-5.2 的一次性数据订正脚本（2026-07-19），刻意不进台账。",
	"20260719_verify_config.sql":     "非 schema 迁移：智谱/火山引擎配置的只读排障验证脚本（2026-07-19），刻意不进台账。",
}

func TestMigrationRegistryStaysContinuous(t *testing.T) {
	regBytes, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("读台账 %s：%v", registryPath, err)
	}
	var files []os.DirEntry
	for _, d := range migrationsDirs {
		fs, err := os.ReadDir(d)
		if err != nil {
			t.Fatalf("读迁移目录 %s：%v", d, err)
		}
		files = append(files, fs...)
	}

	// 台账登记的文件名集合。
	//
	// ⚠️⚠️ 判据必须**逐字照抄权威规则**（verify-migration-checksums.sh:53）：
	//     \|\ [0-9]+\ \|\ \`([0-9]+_[a-zA-Z0-9_]+\.sql)\`\ \|\ \`([0-9a-f]{64})\`\ \|\
	//
	// 224 号第一版写的是宽松版 `^.*\`(name)\`.*$`，结果报出 3 条假缺陷：
	//   - `736_maas_rate_multiplier.sql` 的那一行是 `| 736 | \`name\` | code-landed（…）|`
	//     —— **没有 sha 列**（状态是 code-landed），我的宽松正则把它当"登记了但 sha 不符"；
	//   - `623_...` 命中的是**正文散文里**的反引号文件名，不是表格行
	//     ⇒ 于是报出"台账有磁盘无"，而真相是它被脚本的正则**排除**在外。
	// ⇒ **判据比权威规则更松，就会造出权威规则根本不会报的缺陷**（§168）。
	//   这与 221 号那次是**同一个错误的两个方向**：那次正则太窄（漏），
	//   这次太宽（造假）。**唯一正确的做法是逐字照抄。**
	rowRe := regexp.MustCompile(`(?m)^\|\s+[0-9]+\s+\|\s+\x60([0-9]+_[a-zA-Z0-9_]+\.sql)\x60\s+\|\s+\x60([0-9a-f]{64})\x60\s+\|`)

	// 磁盘上的编号迁移（排除 .down.sql 与非数字前缀）
	var migs []migFile
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".sql") ||
			strings.HasSuffix(f.Name(), ".down.sql") {
			continue
		}
		n, err := strconv.Atoi(strings.SplitN(f.Name(), "_", 2)[0])
		if err != nil {
			continue
		}
		migs = append(migs, migFile{f.Name(), n})
	}
	if len(migs) == 0 {
		t.Fatalf("迁移目录里没有编号迁移：%v", migrationsDirs)
	}

	// 窗口的锚：**必须取自磁盘侧**（磁盘上的最大编号），不是台账侧。
	//
	// ⚠️ 224 号负控 NC-D 实测出来的设计缺陷：
	// 第一版锚点取 `maxRegistered`（台账里已登记的最大编号）。
	// 负控做法是"从台账删掉最大那行（819），模拟忘登记"
	// ⇒ 锚点自动下移到 818 ⇒ **819 被移出窗口** ⇒ 门保持绿。
	// **删掉台账里的登记行，反而让那个缺口从门里消失** ——
	// 这是典型的**自指判据**：用被检查对象自己的状态定义检查范围。
	// （与 §171「验证尺子的断言若复用尺子的返回值」同源：
	//   尺子和被测物同源 ⇒ 尺子错时它跟着错、永远不会响。）
	//
	// 修法：锚点固定取**磁盘最大编号**，台账只作为"被检查的对象"。
	// 于是"磁盘上最新、但台账没登记"必然落在窗口内。
	maxRegistered := -1
	for _, m := range migs {
		if m.num > maxRegistered {
			maxRegistered = m.num
		}
	}
	registeredNames := map[string]bool{}
	for _, m := range rowRe.FindAllStringSubmatch(string(regBytes), -1) {
		registeredNames[m[1]] = true
	}

	// 窗口内逐个表态
	found := 0
	for _, m := range migs {
		if m.num <= maxRegistered-windowSize || m.num > maxRegistered {
			continue
		}
		found++
		if registeredNames[m.name] {
			continue
		}
		if note, exempted := knownUnregistered[m.name]; exempted {
			t.Logf("【已登记缺口，224 号】%s 在近邻窗口内但台账无登记：%s"+
				"⇒ 脚本只会 WARN（verify-migration-checksums.sh:127 不 exit 1），"+
				"部署验证也只查文件存在（deploy/sql/verify-migration.sh）⇒ "+
				"该迁移会被静默部署，无门会响。", m.name, note)
			continue
		}
		t.Errorf("%s（编号 %d，在近邻窗口 [%d, %d] 内）未登记进 %s。\n"+
			"窗口锚点 maxRegistered=%d 取自**磁盘侧最大编号**（刻意不用台账侧：\n"+
			"若锚点取自台账，删掉台账里的登记行会让锚点下移、把这个缺口自己移出窗口\n"+
			"——负控 NC-D 实测过，见本文件里那段注释）。\n"+
			"⚠️ 本仓惯例已明确：800+ 的迁移连续登记（803~819 全部有登记行），\n"+
			"「older files expected to be unregistered」这个前提在近邻窗口**不再成立**。\n"+
			"后果：该迁移会被静默部署（脚本 warn-only + 部署侧只查文件存在）。\n"+
			"修法：按台账既有形态补一行，sha 必须**文件实测值**（不是从别处抄）。\n"+
			"若这是有意不登记，请登记进 knownUnregistered 并写明理由。",
			m.name, m.num, maxRegistered-windowSize+1, maxRegistered,
			registryPath, maxRegistered)
	}
	if found == 0 {
		t.Errorf("近邻窗口内一个迁移都没有（maxRegistered=%d）⇒ "+
			"要么迁移目录结构变了，要么本门的窗口计算已失效。", maxRegistered)
	}
}

// TestRegistryShaIsActuallyTheFile —— 补一条正向纪律：**台账里的 sha 必须是文件实测值**。
//
// 221 号已坐实：台账是**账本**，登记行可能与磁盘脱节；
// 而 221 号自己踩过「以为登记是权威、其实登记滞后于文件」的坑。
// ⇒ 本条按脚本的**真实规则**（同文件多行、任一命中即过，:94-103）重算一遍，
// 口径必须与脚本一致（§168），否则会报出脚本根本不会报的东西。
func TestRegistryShaIsActuallyTheFile(t *testing.T) {
	regBytes, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("读台账：%v", err)
	}
	// filename -> sha 集合。
	// ⚠️ 逐字照抄 verify-migration-checksums.sh:53 的整行正则（理由同上一处判据）：
	// 宽松版会把"只有 code-landed、没有 sha 列"的行、以及正文散文里的
	// 反引号文件名，都误当成登记 ⇒ 报出权威规则根本不会报的东西。
	recorded := map[string]map[string]bool{}
	rowRe := regexp.MustCompile(`(?m)^\|\s+[0-9]+\s+\|\s+\x60([0-9]+_[a-zA-Z0-9_]+\.sql)\x60\s+\|\s+\x60([0-9a-f]{64})\x60\s+\|`)
	for _, m := range rowRe.FindAllStringSubmatch(string(regBytes), -1) {
		if recorded[m[1]] == nil {
			recorded[m[1]] = map[string]bool{}
		}
		recorded[m[1]][m[2]] = true
	}

	checked, mismatched := 0, 0
	for name, shas := range recorded {
		b, err := readMigration(name)
		if err != nil {
			// 台账有、磁盘无 —— 脚本 :113-123 判为 fatal。这里只报，不 exit。
			t.Errorf("台账登记了 %s，但磁盘上读不到 ⇒ 脚本 verify-migration-checksums.sh:113-123 会判 fatal", name)
			continue
		}
		sum := sha256.Sum256(b)
		actual := hex.EncodeToString(sum[:])
		checked++
		hit := false
		for s := range shas {
			if s == actual || (len(s) < len(actual) && strings.HasPrefix(actual, s)) {
				hit = true
				break
			}
		}
		if !hit {
			mismatched++
			var got []string
			for s := range shas {
				got = append(got, s)
			}
			t.Errorf("%s 的登记 sha 与文件实测不符。\n"+
				"  实测   : %s\n  台账登记: %s\n"+
				"⇒ verify-migration-checksums.sh 会判 MISMATCH 并 exit 1 ⇒ CI Unified Verification 失败。\n"+
				"修法：补一行新 sha（保留原行，供已应用库按旧字节核对）——"+
				"参见 730/810/811/812 的既有多行登记惯例。",
				name, actual, strings.Join(got, " | "))
		}
	}
	if checked == 0 {
		t.Fatalf("没有任何登记行能与磁盘对得上（登记 %d 条）⇒ 台账格式或磁盘状态已变。", len(recorded))
	}
	t.Logf("按脚本口径重算：登记 %d 条，实际校验 %d 条，不匹配 %d 条（应与 221 号实测的 161/0 一致或更新）",
		len(recorded), checked, mismatched)
}

func containsString(migs []migFile, s string) bool {
	for _, m := range migs {
		if m.name == s {
			return true
		}
	}
	return false
}
