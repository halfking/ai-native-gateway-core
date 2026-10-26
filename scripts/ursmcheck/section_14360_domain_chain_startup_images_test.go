// R79 轮 · §143.60：**11 张 domain 链的表补 startup 通道镜像**（迁移 895）的登记面门。
//
// 背景（2026-10-22）：
//
//	这 11 张表**只有 domain 通道建**（sql/migrations/domain/ 下的
//	035 / 132 / 133 / 134 / 135 / 220 / 331 七个文件），而
//	**domain 链从来没有在生产上跑过** —— 生产只跑 startup 通道
//	（installer/internal/dbinit/runner.go 的 StartupFiles +
//	scripts/apply-db-revision-sequence.sh 的 files=()）与 installer 的
//	00-prereqs / 01-schema / 02-seed，**domain 通道两者都不含**
//	⇒ 生产 252 的 schema_migrations 里 domain 链的号**一个都没有**
//	⇒ 那 11 张表在生产上**一个都不存在**
//	⇒ 而它们**都有 Go 生产读写点** ⇒ 相关功能在生产上因缺表而 42P01。
//
//	⇒ 895 把这 11 张表按**原形态**镜像到 startup 通道。
//
// ★ 本文件守什么（登记面 + 形态面，两组）：
//
//	【登记面】895 必须被登记在**每一个**投递面上。
//	  ★ 漏一处 = 该迁移在那个通道上**永远不生效** —— 而单条迁移的
//	    判据照常全绿（849/850/851 与 845-851 通道腿那一族的复发面）。
//	  ★ 登记面是**7 个点**，不是 6 个：除 canonical / embeddata /
//
// //	    runner.go / TSV / 投递通道 / 台账 之外，
//
//	    **installer/cmd/llm-gw-installer/main.go 的 go:embed 指令 +
//	    embeddedSQLFiles 映射**也是必登记点
//	    （stats_migrations_test.go 的双向对账会同时守住两个方向，
//	      漏了它 ⇒ installer 装不出这个文件 ⇒ 新装库仍然缺表）。
//
//	【形态面】镜像必须与**域链建出来的形态一致**（不许简化、不许只建列）。
//	  ★ 判据不是「文件里写着 CREATE TABLE」—— 那是**恒绿**判据；
//	    判据是「**逐字**与域链源文件的 DDL 一致」+「基线没有重复建」。
//
// ★ 仓库纪律（§143.20 R65 轮的教训，**本文件全程遵守**）：
//
//	「断言 SQL 文本前必须剥注释」—— 895 的文件头里**引用了**
//	`CREATE TABLE IF NOT EXISTS`、`SET LOCAL statement_timeout` 等字面量
//	（那是它的说明文字）⇒ 不剥注释的话，正向断言会被注释**假满足**。
//	⇒ ⇒ 本文件对**结构性断言**一律剥注释后判；
//	  而对**词法禁令**（CONCURRENTLY / request_logs）一律判**原始全文**
//	  （部署通道的 runtime adapter 是对**整份文件文本含注释**做替换的）。
package ursm

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

const (
	mig895File     = "895_domain_chain_startup_images.sql"
	mig895DownFile = "895_domain_chain_startup_images.down.sql"

	mig895Canonical = "../../sql/migrations/startup/" + mig895File
	mig895DownCanon = "../../sql/migrations/startup/" + mig895DownFile
	mig895Embed     = "../../installer/cmd/llm-gw-installer/embeddata/startup/" + mig895File
	mig895EmbedDown = "../../installer/cmd/llm-gw-installer/embeddata/startup/" + mig895DownFile

	mig895RunnerGo  = "../../installer/internal/dbinit/runner.go"
	mig895MainGo    = "../../installer/cmd/llm-gw-installer/main.go"
	mig895TSV       = "../../sql/schema/installed_startup_migrations.tsv"
	mig895SeqSh     = "../../scripts/apply-db-revision-sequence.sh"
	mig895Changelog = "../../docs/db-changelog.md"
	mig895Baseline  = "../../sql/schema/01-schema.sql"

	mig895DomainDir = "../../sql/migrations/domain/"

	// mig895NoTransactionMarker 是 installer 的「逐文件事务豁免」标记字面量。
	//
	// ★ 它的来源：installer/internal/dbinit/runner.go 的 noTransactionMarker 常量。
	// ★ 本门用它做**禁令**（不是豁免）—— 理由见
	//   TestMigration895HasNoForbiddenTokens 里的说明。
	mig895NoTransactionMarker = "dbinit:no-transaction"
)

// mig895DomainTables 是 895 镜像的 11 张表 → 域链源文件。
//
// ★ 这张表**就是本轮要查清的核心事实**的载体：
//
//	11 张表**全部**只由 domain 通道建 ⇒ 全部要由 895 新建。
var mig895DomainTables = []struct {
	table  string
	source string
}{
	{"client_profiles", mig895DomainDir + "132_client_profile.sql"},
	{"client_behavior_events", mig895DomainDir + "132_client_profile.sql"},
	{"credential_state_nodes", mig895DomainDir + "035_credential_state_management.sql"},
	{"feishu_bot_routing_rules", mig895DomainDir + "220_feishu_bot_routing.sql"},
	{"feishu_bot_send_log", mig895DomainDir + "220_feishu_bot_routing.sql"},
	{"notification_channels", mig895DomainDir + "135_approval_routing.sql"},
	{"provider_incidents", mig895DomainDir + "133_provider_reputation.sql"},
	{"provider_reputation_timeseries", mig895DomainDir + "133_provider_reputation.sql"},
	{"session_credential_rotations", mig895DomainDir + "331_session_state_and_rotations.sql"},
	{"session_state_snapshots", mig895DomainDir + "331_session_state_and_rotations.sql"},
	{"tool_executions", mig895DomainDir + "134_tool_execution.sql"},
}

// mig895SiblingTables 是**同一批域链源文件**里、但 895 **不得**创建的表。
//
// ★ 前三个**已由基线建**（01-schema.sql:17738 / :5550 / :6774）
//
//	⇒ 镜像它们会与基线抢同名对象；
//
// ★ 第四个（notification_send_log）基线未建，但**全仓 Go 零读写点**
//
//	⇒ 不在本条口径内。
var mig895SiblingTables = []string{
	"tool_usage_stats",
	"approval_routing_rules",
	"credential_state_log",
	"notification_send_log",
}

// mig895TriggerFuncs 是 895 必须一并镜像的 6 个触发器函数。
//
// ★ 实测：基线里**一个都没有**（逐个 grep 命中数全 0）
//
//	⇒ 不补则触发器建不出来 ⇒ 必须镜像。
var mig895TriggerFuncs = []string{
	"update_client_profile_updated_at",
	"update_feishu_route_updated_at",
	"update_notification_channels_updated_at",
	"update_provider_reputation_updated_at",
	"calculate_incident_duration",
	"calculate_tool_execution_duration",
}

// mig895Triggers 是 895 必须建齐的 7 个触发器。
var mig895Triggers = []string{
	"trg_client_profiles_updated_at",
	"trg_feishu_route_updated_at",
	"trg_notification_channels_updated_at",
	"trg_provider_rep_ts_updated_at",
	"trg_provider_incidents_updated_at",
	"trg_provider_incidents_duration",
	"trg_tool_executions_duration",
}

// ── helpers ───────────────────────────────────────────────────────────────

// readMig895 读文件并在缺失时**响亮失败**（而不是返回空串让判据空转）。
func readMig895(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	return string(b)
}

// stripMig895Comments 去掉 SQL 注释（-- 行注释与 /* */ 块注释）。
//
// ★ 与 R75 轮的 stripSQLComments 同款（不共用同一个函数名，
//
//	以免本门与同包的另一扇门产生耦合）。
func stripMig895Comments(s string) string {
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

// mig895Normalize 归一化一段 SQL：剥 public. 前缀 + 折叠空白。
//
// ★ 为什么折叠空白：域链源文件与镜像的行宽/缩进不同（镜像多了一级
//
//	schema 限定与对齐注释）⇒ 逐字节比对会误报。
//	★ 但**不**折叠空白就会漏掉「两列写成了一列」这类真差异 ⇒ 只折叠
//	连续空白，不删任何**非空白**字符。
func mig895Normalize(s string) string {
	s = strings.ReplaceAll(s, "public.", "")
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(s, " "))
}

// mig895CreateTableBody 抽出 `CREATE TABLE IF NOT EXISTS <table> (...)` 的表体。
//
// 返回 "" 表示没找到。
func mig895CreateTableBody(t *testing.T, src, table string) string {
	t.Helper()
	body := stripMig895Comments(src)
	re := regexp.MustCompile(`(?is)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+(?:public\.)?` +
		regexp.QuoteMeta(table) + `\s*\((.*?)\n\);`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return mig895Normalize(m[1])
}

// mig895FindBlock 抽出 `startMarker ... endMarker` 之间的文本（找不到返回 ""）。
func mig895FindBlock(src, startMarker, endMarker string) string {
	i := strings.Index(src, startMarker)
	if i < 0 {
		return ""
	}
	j := strings.Index(src[i:], endMarker)
	if j < 0 {
		return ""
	}
	return src[i : i+j]
}

// mig895SHA256 返回文件内容的 sha256 十六进制串。
func mig895SHA256(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// =============================================================================
// 门 1：canonical 两份（up + down）必须存在
// =============================================================================

func TestMigration895CanonicalFilesExist(t *testing.T) {
	for _, p := range []string{mig895Canonical, mig895DownCanon} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("canonical 文件不存在：%s: %v", p, err)
		}
	}
	// ★ 反向自证：读到的内容不能是空的（空文件会让所有「Contains」判据空转）。
	for _, p := range []string{mig895Canonical, mig895DownCanon} {
		if body := readMig895(t, p); strings.TrimSpace(body) == "" {
			t.Errorf("%s 是空文件 ⇒ 下面所有判据都会空转", p)
		}
	}
}

// =============================================================================
// 门 2：7 个登记点逐点存在
// =============================================================================

func TestMigration895RegisteredInEveryDeliverySurface(t *testing.T) {
	const name = mig895File

	// ① embeddata 副本（up + down）
	for _, p := range []string{mig895Embed, mig895EmbedDown} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("installer embeddata/startup/%s 不存在：%v", p, err)
		}
	}

	// ② main.go：go:embed 指令 + embeddedSQLFiles 映射
	//    ★★ 这是**第 7 个登记点**（漏了它 ⇒ installer 装不出这个文件
	//      ⇒ stats_migrations_test.go 的双向对账会红）。
	mainGo := readMig895(t, mig895MainGo)
	if !strings.Contains(mainGo, "//go:embed embeddata/startup/"+name) {
		t.Errorf("installer main.go 缺少 895 的 go:embed 指令 ⇒ installer 不会内嵌这个文件")
	}
	if !strings.Contains(mainGo, `"startup/`+name+`":`) {
		t.Errorf("installer main.go 的 embeddedSQLFiles 映射缺 895 ⇒ installer 装不出这个文件")
	}

	// ③ runner.go：StartupFiles
	runnerGo := readMig895(t, mig895RunnerGo)
	if !strings.Contains(runnerGo, `"`+name+`"`) {
		t.Errorf("installer/internal/dbinit/runner.go 的 StartupFiles 缺 895 ⇒ 新装库不会应用它")
	}

	// ④ 启动链镜像 tsv
	//    ★ 锚定到「数据行」形态（`<索引>\t<文件名>$`），不用裸 Contains
	//      （裸 Contains 会被 `895_....sql.bak` 之类的前缀命中蒙混过去）。
	if !regexp.MustCompile(`(?m)^\d+\t` + regexp.QuoteMeta(name) + `$`).
		MatchString(readMig895(t, mig895TSV)) {
		t.Errorf("%s 缺 895 的数据行 ⇒ 仓内多道以该 TSV 为准的门会读不到它", mig895TSV)
	}

	// ⑤ 生产升级序列
	//    ★ 锚定到**通道数组里那条完整路径**，不用裸 Contains。
	if !strings.Contains(readMig895(t, mig895SeqSh),
		`"$ROOT_DIR/sql/migrations/startup/`+name+`"`) {
		t.Errorf("%s 的 files=() 缺 895 ⇒ 生产升级链不会应用它", mig895SeqSh)
	}

	// ⑥ sql 变更台账（sha256 登记表）
	//    ★ 锚定到**台账行**形态，不用裸 Contains。
	if !mig895ChangelogRowRE.MatchString(
		strings.Join(filterMig895Lines(readMig895(t, mig895Changelog),
			func(l string) bool { return strings.Contains(l, name) }), "\n")) {
		t.Errorf("%s 缺 895 的登记行 ⇒ SQL 变更没有进唯一 sha256 登记表", mig895Changelog)
	}
}

// filterMig895Lines 挑出满足 pred 的行。
func filterMig895Lines(s string, pred func(string) bool) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if pred(l) {
			out = append(out, l)
		}
	}
	return out
}

// =============================================================================
// 门 3：embeddata 副本与 canonical 逐字一致（sha256 相等）
// =============================================================================

func TestMigration895EmbeddedCopiesAreByteIdentical(t *testing.T) {
	pairs := []struct{ canonical, embed string }{
		{mig895Canonical, mig895Embed},
		{mig895DownCanon, mig895EmbedDown},
	}
	for _, p := range pairs {
		can := mig895SHA256(t, p.canonical)
		emb := mig895SHA256(t, p.embed)
		if can != emb {
			t.Errorf("%s 与 embeddata 副本 sha256 不等：\n  canonical = %s\n  embeddata = %s\n"+
				"★ embeddata 副本是 installer 真正装出去的那份 ⇒ 漂了就等于装出另一个版本",
				p.canonical, can, emb)
		}
	}
}

// =============================================================================
// 门 4：changelog 里记的 sha256 == 实测值
// =============================================================================

// mig895ChangelogRowRE 是 scripts/verify-migration-checksums.sh 用的**同一个**判据。
// ★ 刻意复用它的形状：台账行的格式变了，两边要一起红。
var mig895ChangelogRowRE = regexp.MustCompile(`^\|\s([0-9]+)\s\|\s` + "`" +
	`([0-9]+_[a-zA-Z0-9_]+\.sql)` + "`" + `\s\|\s` + "`" + `([0-9a-f]{64})` + "`" + `\s\|\s`)

func TestMigration895ChangelogRecordsTheMeasuredSHA(t *testing.T) {
	measured := mig895SHA256(t, mig895Canonical)

	var found bool
	for _, line := range strings.Split(readMig895(t, mig895Changelog), "\n") {
		m := mig895ChangelogRowRE.FindStringSubmatch(line)
		if m == nil || m[2] != mig895File {
			continue
		}
		found = true
		if m[3] != measured {
			t.Errorf("台账里 895 记的 sha256 = %s，实测 = %s\n"+
				"★ 台账是唯一 sha256 登记表 ⇒ 记错就等于「这台机器上装的是哪一版」不可判定",
				m[3], measured)
		}
		if m[1] != "895" {
			t.Errorf("台账里 895 那一行的序号是 %q，应为 \"895\"", m[1])
		}
	}
	// ★ 反向自证：必须**真的找到**那一行（否则上面的循环空转 = 恒绿）。
	if !found {
		t.Fatalf("%s 里没有一行匹配台账行判据且文件名为 %s\n"+
			"★ 找不到 = 「台账根本没登记」= 恒绿 ⇒ 必须响亮失败", mig895Changelog, mig895File)
	}
}

// =============================================================================
// 门 5：TSV 的顺序索引与实际行号一致
// =============================================================================

func TestMigration895TSVOrderIndexMatchesItsLinePosition(t *testing.T) {
	lines := strings.Split(readMig895(t, mig895TSV), "\n")

	// 先数出「表头段」的行数（注释行 + 空行），它们都在数据行之前。
	header := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "#") || strings.TrimSpace(l) == "" {
			header++
			continue
		}
		break
	}

	// 数据行：序号必须是 0-based 的连续序列（这个 TSV 是**有序清单**）。
	ordinal := 0
	var saw895 bool
	var saw895Idx int
	for i, l := range lines {
		if strings.HasPrefix(l, "#") || strings.TrimSpace(l) == "" {
			continue
		}
		idxStr, name, ok := strings.Cut(l, "\t")
		if !ok {
			t.Fatalf("%s 第 %d 行没有 tab 分隔符：%q", mig895TSV, i+1, l)
		}
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			t.Fatalf("%s 第 %d 行的索引 %q 不是数字", mig895TSV, i+1, idxStr)
		}
		if idx != ordinal {
			t.Errorf("%s 第 %d 行的索引是 %d，应为 %d\n"+
				"★ 这个 TSV 是**有序清单**（installer 按索引顺序应用）⇒ 索引错了 ⇒ 应用顺序就错了",
				mig895TSV, i+1, idx, ordinal)
		}
		// 索引 == 数据行序号 ⇒ 该数据行的**文件行号**必须 = 表头行数 + 序号 + 1。
		if want := header + ordinal + 1; i+1 != want {
			t.Errorf("%s：索引 %d 的数据行落在文件第 %d 行，按「表头 %d 行 + 序号 %d」应为第 %d 行",
				mig895TSV, idx, i+1, header, ordinal, want)
		}
		if name == mig895File {
			saw895 = true
			saw895Idx = idx
		}
		ordinal++
	}

	// ★ 反向自证：数据行数必须 > 0（否则上面整个循环空转 = 恒绿）。
	if ordinal == 0 {
		t.Fatalf("%s 没有任何数据行 ⇒ 判据空转（量具失效）", mig895TSV)
	}
	if !saw895 {
		t.Fatalf("%s 里没有 895 的数据行 ⇒ installer 不会应用它", mig895TSV)
	}

	// ★ 交叉核对：TSV 的索引必须等于 895 在 runner.go 的 StartupFiles 里的位置。
	//   （TSV 是从 StartupFiles 派生的；两边错位 = installer 的应用顺序与台账说的不是一回事。）
	runnerGo := readMig895(t, mig895RunnerGo)
	block := mig895FindBlock(runnerGo, "StartupFiles: []string{", "\n\t\t},")
	if block == "" {
		t.Fatalf("%s 里找不到 StartupFiles 块 ⇒ 判据失效（结构变了？）", mig895RunnerGo)
	}
	pos := -1
	for i, m := range regexp.MustCompile(`"([^"]+\.sql)"`).FindAllStringSubmatch(block, -1) {
		if m[1] == mig895File {
			pos = i
			break
		}
	}
	if pos < 0 {
		t.Fatalf("%s 的 StartupFiles 里没有 895 ⇒ 新装库不会应用它", mig895RunnerGo)
	}
	if pos != saw895Idx {
		t.Errorf("895 的位置不一致：runner.go 的 StartupFiles 第 %d 位，TSV 的索引是 %d\n"+
			"★ TSV 是从 StartupFiles 派生的 ⇒ 两边错位 = installer 的应用顺序与台账说的不是一回事",
			pos, saw895Idx)
	}
}

// =============================================================================
// 门 6：词法禁令 —— 不得出现 CONCURRENTLY / request_logs 族名
// =============================================================================

// mig895ForbiddenNames 是部署通道 runtime adapter 会对**整份文件文本含注释**
// 做词法替换的那一组名字（见 scripts/apply-db-revision-sequence.sh 的
// `names = r'request_logs(?:_bodies_hot|_bodies|_hot|_archive|_id_seq)?'`）。
//
// ★ 顺序有意义：**长名在前**（`_bodies_hot` 必须先于 `_bodies` 命中）。
var mig895ForbiddenNames = []string{
	"request_logs_bodies_hot",
	"request_logs_bodies",
	"request_logs_hot",
	"request_logs_archive",
	"request_logs_id_seq",
	"request_logs",
}

// mig895IsIdentChar 复刻 Python 正则的 `[\w$]`（Python 3 的 \w 是 Unicode 感知的）。
func mig895IsIdentChar(r rune) bool {
	return r == '$' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// mig895FindForbidden 返回命中的第一个禁用词（没有则 ""）。
//
// ★★ 为什么不能用 Go 正则直接写：那个 adapter 用的判据带**负向回顾断言**
//
//	`(?<![\w$])…(?![\w$])`，而 **Go 的 RE2 不支持回顾断言**
//	（`regexp.MustCompile` 直接报 "invalid named capture"）。
//	⇒ ⇒ 本实现**手工**做同样的边界判定：
//	  · 名字**前**一个字符不能是 `[\w$]`（⇒ `_request_logs` 这种
//	    **已经改过名**的形态**不算命中** —— 与 adapter 的语义一致，
//	    它对已改名标识符是**故意不改写**的）；
//	  · 名字**后**一个字符不能是 `[\w$]`。
func mig895FindForbidden(s string) string {
	if strings.Contains(strings.ToUpper(s), "CONCURRENTLY") {
		return "CONCURRENTLY"
	}
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		for _, name := range mig895ForbiddenNames {
			nameRunes := []rune(name)
			if i+len(nameRunes) > len(runes) {
				continue
			}
			if string(runes[i:i+len(nameRunes)]) != name {
				continue
			}
			if i > 0 && mig895IsIdentChar(runes[i-1]) {
				continue
			}
			end := i + len(nameRunes)
			if end < len(runes) && mig895IsIdentChar(runes[end]) {
				continue
			}
			return name
		}
	}
	return ""
}

func TestMigration895HasNoForbiddenTokens(t *testing.T) {
	for _, p := range []string{mig895Canonical, mig895DownCanon} {
		body := readMig895(t, p)
		if tok := mig895FindForbidden(body); tok != "" {
			t.Errorf("%s 里出现了禁用词 %q\n"+
				"★ CONCURRENTLY：startup 迁移默认在事务里跑 ⇒ 那个选项在事务块内直接报错\n"+
				"  （除非文件声明那条「逐文件事务豁免」标记，而那需要额外的门与理由）\n"+
				"  ★★ 本门判的是**原始全文（含注释）** ⇒ 连在注释里提到它也会红。\n"+
				"    这是刻意的：让判据变成机械检查，不必区分「注释里提到」与「真的用了」\n"+
				"    （⇒ 895 的文件头因此把那个选项称作「并发建索引」而不写它的字面量）。\n"+
				"★ request_logs 族名：部署通道的 runtime adapter 会对**整份文件文本含注释**\n"+
				"  做词法替换 ⇒ 命中即改写执行体 ⇒ 与源不再逐字相同",
				p, tok)
		}

		// ★★★ 逐文件事务豁免标记：**本轮真的踩到了**（不是假想）。
		//
		//	installer/internal/dbinit/runner.go 的 requiresNoTransaction
		//	是**按注释行**扫字面量 `dbinit:no-transaction` 的
		//	⇒ ★★ **哪怕只是在注释里「解释」这个标记**，也会让 installer
		//	  误判本文件「声明了豁免」⇒ applySQL 就不再加 `--single-transaction`
		//	  ⇒ ★★★ **本文件的执行语义被静默改掉**（而门只报「豁免必须窄」，
		//	    读起来像风格问题，实际是行为变更）。
		//	⇒ ⇒ 所以 895 的文件头**刻意不写那个字面量**，改写它的常量名。
		if strings.Contains(body, mig895NoTransactionMarker) {
			t.Errorf("%s 里出现了逐文件事务豁免标记的字面量\n"+
				"★ installer 的 requiresNoTransaction 是**按注释行**扫这个字面量的\n"+
				"  ⇒ 连在注释里解释它也会被判定「声明了豁免」\n"+
				"  ⇒ applySQL 就不再加 `--single-transaction` ⇒ 本文件的执行语义被静默改掉\n"+
				"★ 895 没有不可事务化的语句（它要的就是事务包裹）⇒ **根本不需要**那条豁免。",
				p)
		}
	}

	// ★★ 反向自证（防「恒绿门」）：探测器必须**确实能抓到**这两种词。
	//   若探测器坏了，上面那个循环会永远绿 ⇒ 本门形同虚设。
	for _, probe := range []string{
		"CREATE INDEX CONCURRENTLY idx_x ON t(c);",
		"SELECT 1 FROM request_logs;",
		"SELECT 1 FROM request_logs_bodies;",
		"SELECT 1 FROM request_logs_hot;",
	} {
		if mig895FindForbidden(probe) == "" {
			t.Errorf("★ 探测器失效：对 %q 没能报出禁用词 ⇒ 上面那道判据是恒绿的", probe)
		}
	}
	// ★ 阳性对照的另一半：一个**干净**的样本必须**不**被报出来
	//   （否则探测器会变成「见什么都红」，同样无效）。
	for _, clean := range []string{
		"CREATE INDEX IF NOT EXISTS idx_x ON public.client_profiles(tenant_id);",
		"SELECT request_id FROM public.tool_executions;",
		// ★ 已改名的形态（`_request_logs`）**不算命中** —— 与 adapter 的
		//   负向回顾断言语义一致（它对已改名标识符故意不改写）。
		"SELECT 1 FROM public._request_logs_hot;",
	} {
		if tok := mig895FindForbidden(clean); tok != "" {
			t.Errorf("★ 探测器误报：干净样本 %q 被报成 %q ⇒ 判据没有判别力", clean, tok)
		}
	}
}

// =============================================================================
// 门 7：必须有事务包裹 + lock_timeout + statement_timeout
// =============================================================================

func TestMigration895HasTransactionAndTimeoutBudget(t *testing.T) {
	for _, p := range []string{mig895Canonical, mig895DownCanon} {
		body := stripMig895Comments(readMig895(t, p))

		// ★ 剥了注释才判：895 的文件头里**引用了**这些字面量（说明文字）
		//   ⇒ 不剥注释的话，正向断言会被注释**假满足**。
		if !strings.Contains(body, "BEGIN;") {
			t.Errorf("%s 里没有 BEGIN; ⇒ 不是整条原子", p)
		}
		if !strings.Contains(body, "COMMIT;") {
			t.Errorf("%s 里没有 COMMIT; ⇒ 事务没有收口", p)
		}
		if !strings.Contains(body, "lock_timeout") {
			t.Errorf("%s 里没有 lock_timeout ⇒ 可能无限期等锁堵住全库（§44）", p)
		}
		if !strings.Contains(body, "statement_timeout") {
			t.Errorf("%s 里没有 statement_timeout ⇒ 共享 PG 服务端默认 30s 会杀掉长语句\n"+
				"★ 见 sql/migrations/startup/855_session_analytics_time_window.sql 的文件头",
				p)
		}
		// BEGIN; 必须排在 COMMIT; 之前（否则「顺序反了」也是一种形态漂移）。
		bi := strings.Index(body, "BEGIN;")
		ci := strings.Index(body, "COMMIT;")
		if bi < 0 || ci < 0 || bi > ci {
			t.Errorf("%s：BEGIN;(pos=%d) 与 COMMIT;(pos=%d) 的顺序不对", p, bi, ci)
		}

		// ★★ 超时预算必须**钉住数值**，不能只钉「出现过」。
		//
		//	★ 变异 M12 实测踩到：把 `statement_timeout = '300s'` 改成 `'30s'`
		//	  ⇒ 「文件里出现过 statement_timeout」这个判据**照样绿**
		//	  ⇒ 门没有牙。
		//	⇒ ⇒ 这里改成解析字面量并比大小。
		//
		//	★ 下界的依据：共享 PG 服务端**默认 30s** statement_timeout
		//	  （见 sql/migrations/startup/855_session_analytics_time_window.sql
		//	   的文件头：2026-10-13 在 245 实测，超时点就是第一条
		//	   CREATE MATERIALIZED VIEW）⇒ **必须严格大于 30s**，
		//	   否则这条迁移等于没有自带预算。
		st := mig895TimeoutSeconds(t, body, "statement_timeout")
		if st <= 30 {
			t.Errorf("%s 的 statement_timeout = %ds，必须**严格大于 30s**\n"+
				"★ 共享 PG 服务端默认 30s ⇒ 30s 等于「没有自带预算」\n"+
				"★ 依据：sql/migrations/startup/855_session_analytics_time_window.sql 的文件头",
				p, st)
		}
		// lock_timeout 同理：必须有**有限**的上界（§44 的教训：
		// 一次没有上界的锁等待把 14 个网关会话堵了约 1.5 分钟）。
		lt := mig895TimeoutSeconds(t, body, "lock_timeout")
		if lt <= 0 {
			t.Errorf("%s 的 lock_timeout = %ds，必须是一个**正数**上界\n"+
				"★ §44 教训：没有上界的锁等待会堵住全库 ⇒ 宁可让迁移失败（55P03）",
				p, lt)
		}
	}
}

// mig895TimeoutSeconds 解析 `SET LOCAL <name> = '<literal>'` 并折算成秒。
//
// ★ 支持 '30s' / '30min' / '1800'（秒）/ '1h' / '500ms'。
func mig895TimeoutSeconds(t *testing.T, body, name string) int {
	t.Helper()
	re := regexp.MustCompile(`SET\s+LOCAL\s+` + regexp.QuoteMeta(name) + `\s*=\s*'([^']+)'`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("★ %s 里找不到 `SET LOCAL %s = '...'` ⇒ 判据失效", name, name)
	}
	lit := strings.TrimSpace(m[1])
	unit := "s"
	for _, u := range []string{"ms", "s", "min", "h"} {
		if strings.HasSuffix(lit, u) {
			unit = u
			lit = strings.TrimSuffix(lit, u)
			break
		}
	}
	n, err := strconv.Atoi(strings.TrimSpace(lit))
	if err != nil {
		t.Fatalf("看不懂的 %s 字面量：%q", name, m[1])
	}
	switch unit {
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

// =============================================================================
// 门 8：镜像建齐 11 张表，且【只】建这 11 张
// =============================================================================

func TestMigration895CreatesTheElevenTablesAndOnlyThose(t *testing.T) {
	body := stripMig895Comments(readMig895(t, mig895Canonical))

	// ★★ 为什么这里**不能**用 strings.Contains（★ 变异 M5 实测踩到）：
	//
	//	M5 把建表语句改名成 `public.tool_executions_disabled`
	//	⇒ 而 `Contains(body, "CREATE TABLE IF NOT EXISTS public.tool_executions")`
	//	  **照样成立**（前者是后者的**前缀**）⇒ 判据没有牙。
	//	⇒ ⇒ 必须**锚定**：表名后面必须紧跟列清单的开括号 `(`。
	//	  （这与 §143.35 的 cron 那条教训同型：「路径出现在文件里」
	//	    ≠「它被调度了」⇒ 判据必须落到**结构**上，不能落到子串上。）
	for _, tt := range mig895DomainTables {
		re := regexp.MustCompile(`(?is)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+(?:public\.)?` +
			regexp.QuoteMeta(tt.table) + `\s*\(`)
		if !re.MatchString(body) {
			t.Errorf("895 里没有 `CREATE TABLE IF NOT EXISTS public.%s (` ⇒ 这张表在生产上仍然缺失\n"+
				"★ 域链源文件：%s", tt.table, tt.source)
		}
	}
	for _, t2 := range mig895SiblingTables {
		re := regexp.MustCompile(`(?is)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+(?:public\.)?` +
			regexp.QuoteMeta(t2) + `\s*\(`)
		if re.MatchString(body) {
			t.Errorf("895 里建了 `public.%s` ⇒ 它是**基线已经建的表**（或不在本条口径内的表）\n"+
				"★ 镜像它们会与基线抢同名对象 ⇒ 迁移在基线已建表的库上行为不可预期",
				t2)
		}
	}
}

// =============================================================================
// 门 9：★ 形态一致 —— 镜像的表体/函数体/触发器/索引必须与域链源文件逐字相同
// =============================================================================

func TestMigration895ShapeMatchesTheDomainChainSources(t *testing.T) {
	mirror := readMig895(t, mig895Canonical)

	for _, tt := range mig895DomainTables {
		domain := readMig895(t, tt.source)
		want := mig895CreateTableBody(t, domain, tt.table)
		got := mig895CreateTableBody(t, mirror, tt.table)
		if want == "" {
			t.Fatalf("域链源文件 %s 里抽不到表 %s 的建表体 ⇒ 判据失效", tt.source, tt.table)
		}
		if got == "" {
			t.Errorf("895 里抽不到表 %s 的建表体 ⇒ 这张表没被建出来", tt.table)
			continue
		}
		if want != got {
			t.Errorf("表 %s 的建表体与域链源文件 %s **不一致** ⇒ 镜像不是「原形态」\n"+
				"★ 不许简化、不许只建列：镜像必须与域链建出来的形态一致。\n"+
				"  domain : %s\n  mirror : %s",
				tt.table, tt.source, want, got)
		}
	}

	// 触发器函数：函数体必须逐字相同（6 个）。
	mirrorBody := stripMig895Comments(mirror)
	for _, fn := range mig895TriggerFuncs {
		domFn := mig895FuncBody(readMig895(t, mig895DomainDir+"132_client_profile.sql")+
			readMig895(t, mig895DomainDir+"035_credential_state_management.sql")+
			readMig895(t, mig895DomainDir+"220_feishu_bot_routing.sql")+
			readMig895(t, mig895DomainDir+"135_approval_routing.sql")+
			readMig895(t, mig895DomainDir+"133_provider_reputation.sql")+
			readMig895(t, mig895DomainDir+"331_session_state_and_rotations.sql")+
			readMig895(t, mig895DomainDir+"134_tool_execution.sql"), fn)
		mirFn := mig895FuncBody(mirrorBody, fn)
		if domFn == "" {
			t.Fatalf("域链源文件里抽不到函数 %s 的函数体 ⇒ 判据失效", fn)
		}
		if mirFn == "" {
			t.Errorf("895 里没有触发器函数 %s ⇒ 触发器建不出来", fn)
			continue
		}
		if domFn != mirFn {
			t.Errorf("触发器函数 %s 的函数体与域链不一致\n  domain : %s\n  mirror : %s", fn, domFn, mirFn)
		}
	}

	// 触发器：定义必须逐字相同（7 个）。
	for _, tg := range mig895Triggers {
		want := mig895TriggerDef(readMig895(t, mig895DomainDir+"132_client_profile.sql")+
			readMig895(t, mig895DomainDir+"220_feishu_bot_routing.sql")+
			readMig895(t, mig895DomainDir+"135_approval_routing.sql")+
			readMig895(t, mig895DomainDir+"133_provider_reputation.sql")+
			readMig895(t, mig895DomainDir+"134_tool_execution.sql"), tg)
		got := mig895TriggerDef(mirrorBody, tg)
		if want == "" {
			t.Fatalf("域链源文件里抽不到触发器 %s ⇒ 判据失效", tg)
		}
		if got == "" {
			t.Errorf("895 里没有触发器 %s", tg)
			continue
		}
		if want != got {
			t.Errorf("触发器 %s 的定义与域链不一致\n  domain : %s\n  mirror : %s", tg, want, got)
		}
	}
}

// mig895FuncBody 抽出 `CREATE OR REPLACE FUNCTION <fn>() RETURNS TRIGGER AS $$...$$` 的函数体。
func mig895FuncBody(src, fn string) string {
	re := regexp.MustCompile(`(?is)CREATE\s+OR\s+REPLACE\s+FUNCTION\s+(?:public\.)?` +
		regexp.QuoteMeta(fn) + `\s*\(\s*\)\s*RETURNS\s+TRIGGER\s+AS\s+\$\$(.*?)\$\$\s*LANGUAGE\s+plpgsql;`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return ""
	}
	return mig895Normalize(m[1])
}

// mig895TriggerDef 抽出 `CREATE TRIGGER <tg> ...;` 的定义（归一化）。
func mig895TriggerDef(src, tg string) string {
	re := regexp.MustCompile(`(?is)CREATE\s+TRIGGER\s+` + regexp.QuoteMeta(tg) + `\s+(.*?);`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return ""
	}
	return mig895Normalize(m[1])
}

// =============================================================================
// 门 10：★ 幂等形态 —— 每条建表/建索引都带 IF NOT EXISTS，每个触发器都先 DROP
// =============================================================================

func TestMigration895IsIdempotentInShape(t *testing.T) {
	body := stripMig895Comments(readMig895(t, mig895Canonical))

	// ① 每条 CREATE TABLE 都必须带 IF NOT EXISTS。
	for _, m := range regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(IF\s+NOT\s+EXISTS\s+)?([A-Za-z_."]+)`).
		FindAllStringSubmatch(body, -1) {
		if m[1] == "" {
			t.Errorf("895 里有 `CREATE TABLE %s` 没有 IF NOT EXISTS ⇒ 第二次跑报 42P07", m[2])
		}
	}
	// ② 每条 CREATE INDEX 都必须带 IF NOT EXISTS。
	for _, m := range regexp.MustCompile(`(?is)CREATE\s+INDEX\s+(IF\s+NOT\s+EXISTS\s+)?`).
		FindAllStringSubmatch(body, -1) {
		if m[1] == "" {
			t.Errorf("895 里有 `CREATE INDEX` 没有 IF NOT EXISTS ⇒ 第二次跑报 42P07")
		}
	}
	// ③ 每个 CREATE TRIGGER 前必须紧邻一条针对同一触发器的 DROP TRIGGER IF EXISTS。
	//    ★ PostgreSQL 没有 CREATE TRIGGER IF NOT EXISTS ⇒ 不先 DROP 就建不出第二次。
	for _, tg := range mig895Triggers {
		re := regexp.MustCompile(`(?is)DROP\s+TRIGGER\s+IF\s+EXISTS\s+` + regexp.QuoteMeta(tg) + `\b`)
		if !re.MatchString(body) {
			t.Errorf("895 里建触发器 %s 之前没有 `DROP TRIGGER IF EXISTS %s`\n"+
				"★ PostgreSQL 没有 CREATE TRIGGER IF NOT EXISTS ⇒ 第二次跑报 42710 duplicate_trigger",
				tg, tg)
		}
	}
	// ④ 每个触发器函数都必须是 CREATE OR REPLACE（幂等）。
	for _, fn := range mig895TriggerFuncs {
		re := regexp.MustCompile(`(?is)CREATE\s+OR\s+REPLACE\s+FUNCTION\s+(?:public\.)?` +
			regexp.QuoteMeta(fn) + `\b`)
		if !re.MatchString(body) {
			t.Errorf("895 里没有 `CREATE OR REPLACE FUNCTION %s` ⇒ 第二次跑报 42723", fn)
		}
	}
}

// =============================================================================
// 门 11：★ 基线交叉核对 —— 11 张表基线确实没建；3 张兄弟表基线确实建了
// =============================================================================

func TestMigration895BaselineCrossCheck(t *testing.T) {
	baseline := readMig895(t, mig895Baseline)

	// ① 11 张表：基线**不得**建（否则镜像会与基线抢同名对象）。
	for _, tt := range mig895DomainTables {
		re := regexp.MustCompile(`(?im)CREATE\s+TABLE\s+(IF\s+NOT\s+EXISTS\s+)?(public\.)?` +
			regexp.QuoteMeta(tt.table) + `\s*\(`)
		if re.MatchString(baseline) {
			t.Errorf("基线 %s 里其实建了 %s ⇒ 895 不该再建它（会与基线抢同名对象）",
				mig895Baseline, tt.table)
		}
	}
	// ② 3 张兄弟表：基线**必须**建（这是「不镜像它们」这条决定的依据）。
	for _, t2 := range []string{"tool_usage_stats", "approval_routing_rules", "credential_state_log"} {
		re := regexp.MustCompile(`(?im)CREATE\s+TABLE\s+(IF\s+NOT\s+EXISTS\s+)?(public\.)?` +
			regexp.QuoteMeta(t2) + `\s*\(`)
		if !re.MatchString(baseline) {
			t.Errorf("基线 %s 里其实**没有**建 %s ⇒ 「它已由基线建、所以 895 不镜像它」这条依据不成立",
				mig895Baseline, t2)
		}
	}
	// ★ 反向自证：上面的①必须**看得见东西**（否则「基线没建」是恒绿结论）。
	if !regexp.MustCompile(`(?im)CREATE\s+TABLE\s+(IF\s+NOT\s+EXISTS\s+)?(public\.)?credentials\s*\(`).
		MatchString(baseline) {
		t.Errorf("基线 %s 里连 credentials 都找不到 ⇒ 判据失效（路径漂移或格式变了？）", mig895Baseline)
	}
}
