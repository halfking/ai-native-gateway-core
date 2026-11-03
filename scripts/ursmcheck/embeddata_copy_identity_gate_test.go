package ursm_test

// embeddata_copy_identity_gate_test.go —— 「embeddata 副本 ≡ canonical」通用门
// （2026-11-03 第四十一轮）
//
// ★★★ **为什么要有这道门（★ 本轮的触发点）**：
//
//	第四十轮量到：★ `installer/cmd/llm-gw-installer/embeddata/startup/` 里
//	**492 个文件在 `sql/migrations/startup/` 有同名 canonical** ⇒ ★ 两侧是
//	**两份副本**（★ 一份进新装安装器的字节、★ 一份进升级通道的字节）。
//	⇒ ★ 而**仓内只有 3 处**逐字节比对（★ 876 / 892 / 895）⇒ ★ **489 对无护栏**。
//	⇒ ★★ **930 恰好就落在这个盲区里**：★ 它是合并时的 **add/add 冲突**
//	  （★ 两侧各自新增了同名文件、★ 内容不同）⇒ ★★ 若解冲突时两侧解得**不一致**
//	  ⇒ ★ **新装库与升级库会跑出不同的字节** ⇒ ★ 而**没有任何门会红**。
//	⇒ ⇒ ★★ 这道门把护栏面从 **3 对**扩到 **492 对**（★ 覆盖 163 倍）。
//
// ★★★ **判据（★ 逐条都实测过、★ 面是干净的）**：
//
//	① 凡 embeddata 目录里【有迁移号前缀】的文件（★ `^\d`）：
//	   canonical 侧（★ `sql/migrations/startup/`【递归】、★ 含 `up/` 与 `down/` 子目录）
//	   必须有同名文件、★ 且 **sha256 逐字相同**。
//	② 【无迁移号前缀】的文件 = **installer-only 资产**（★ 按设计没有 canonical）
//	   ⇒ 豁免，★ 但必须出现在下面的**声明清单**里（★ 附理由）⇒ ★ 不许悄悄多出来。
//
// ★★★ **为什么递归找 canonical（★ 量出来的两个特例）**：
//
//	· `600_outbound_body_to_bodies_hot.sql` 的 canonical 在
//	  `sql/migrations/startup/**up**/` 子目录里（★ 不在顶层）⇒ ⇒ 只查顶层会**假红**。
//	· `session_turns_hot_bootstrap.sql` **全仓只有 embeddata 一份** ⇒ ★ 它的头部
//	  自述「★ Fresh-installer bootstrap … 它不是升级迁移、★ 不得用于已有部署」
//	  ⇒ ★★ 按设计就没有 canonical ⇒ ⇒ 只能**按形状豁免**（★ 无迁移号前缀）。
//
// ★★★ **为什么「无前缀 ⇒ installer-only」是结构信号而不是猜名字**：
//
//	★ 实测：★ embeddata 494 个文件里 **493 个有迁移号前缀**、★ **恰好只有 1 个没有**
//	⇒ ⇒ ⇒ **一一对应**（★ 493 : 1）⇒ ★★ 判据落在**编号约定**上、★ 不落在文件名上。
//
// ★★★ **本门的牙（★ 合成夹具）**：
//
//	★ 判据写成**纯函数**（★ `embCopyJudge`）⇒ ⇒ 合成夹具可以在**临时目录**上跑
//	（★ 仓内真文件零改动）⇒ ★ 变异臂必须能把它判红。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	embCopyEmbedRel = "../../installer/cmd/llm-gw-installer/embeddata/startup"
	embCopyCanonRel = "../../sql/migrations/startup"

	// ★★ 判据面【不得退化】的下限：★ 实测 493 个有前缀的文件。
	//   ★ 这是**下限**、★ 不是「从清单抄来的基线」（★ 抄基线 = 自证）。
	embCopyMinJudged = 400
)

// ★★ embCopyInstallerOnly 声明「按设计【没有】canonical 副本」的 installer-only 资产。
//
// ★ 规则：★ 新增条目必须写清理由；★ 删除条目必须先确认那份资产真的不见了。
// ★★ 门会【双向】核对：★ 声明里有的、★ 目录里没有 ⇒ 冗余声明（红）；
//
//	★ 目录里有的、★ 声明里没有 ⇒ 悄悄多出来的资产（红）。
var embCopyInstallerOnly = map[string]string{
	"session_turns_hot_bootstrap.sql": "installer-only 资产：★ 头部自述「Fresh-installer bootstrap：final Session V2 hot-path 对象；★ 它不是升级迁移、★ 不得用于已有部署」⇒ ★ 按设计没有 sql/migrations 侧的 canonical",
}

// embCopyViolation 一条判据违例。
type embCopyViolation struct {
	File   string
	Reason string
}

func (v embCopyViolation) String() string {
	return fmt.Sprintf("%s：%s", v.File, v.Reason)
}

// embCopySHA256 读文件并算 sha256（★ 读不出来 ⇒ 返回空串，★ 由调用方判红）。
func embCopySHA256(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// embCopyIsMigrationNumbered 判「有没有迁移号前缀」。
//
// ★★ 为什么用「首字符是数字」而不是解析前缀：★ 迁移号有 3 位（★ `930_…`）
//
//	也有别的形态（★ `202609_01_…`）⇒ ⇒ 按**首字符**判最稳、★ 且不漏任何形态。
func embCopyIsMigrationNumbered(name string) bool {
	if name == "" {
		return false
	}
	c := name[0]
	return c >= '0' && c <= '9'
}

// embCopyIndexCanonical 递归索引 canonical 目录：★ 文件名 → 全部路径。
//
// ★★ 递归是必需的（★ `up/` 与 `down/` 子目录里有 13 + 13 个文件）。
//
//	⇒ ★ 返回**全部**路径（★ 不取第一个）⇒ ★ 判据层决定「至少有一处逐字相同」。
func embCopyIndexCanonical(canonRoot string) map[string][]string {
	out := map[string][]string{}
	_ = filepath.Walk(canonRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		name := filepath.Base(p)
		out[name] = append(out[name], p)
		return nil
	})
	return out
}

// embCopyJudge ★★ **本门的判据本体（★ 纯函数）**。
//
// ★ 输入：★ embeddata 目录 ＋ canonical 索引；★ 输出：★ 违例清单。
// ★ 刻意不接 *testing.T ⇒ ⇒ 合成夹具能在**临时目录**上用**同一份**判据跑。
func embCopyJudge(embedDir string, canon map[string][]string) []embCopyViolation {
	var out []embCopyViolation

	entries, err := os.ReadDir(embedDir)
	if err != nil {
		return []embCopyViolation{{
			File:   embedDir,
			Reason: fmt.Sprintf("★ 读不到 embeddata 目录：%v ⇒ 判据的分母为 0 ⇒ 不可判定", err),
		}}
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()

		// ② 无迁移号前缀 ⇒ installer-only 资产 ⇒ 必须在声明清单里。
		if !embCopyIsMigrationNumbered(name) {
			if _, ok := embCopyInstallerOnly[name]; !ok {
				out = append(out, embCopyViolation{
					File: name,
					Reason: "★ 无迁移号前缀 ⇒ 按形状属 installer-only 资产 ⇒ " +
						"★ 但它【不在】声明清单里 ⇒ ⇒ 悄悄多出来的一份 ⇒ 必须显式声明并写理由",
				})
			}
			continue
		}

		// ① 有迁移号前缀 ⇒ canonical 必须存在且逐字相同。
		paths := canon[name]
		if len(paths) == 0 {
			out = append(out, embCopyViolation{
				File: name,
				Reason: "★ embeddata 里有、★ canonical 侧【递归找不到同名文件】⇒ " +
					"⇒ 这份副本的来源丢了 ⇒ ⇒ 新装通道会跑一份【无出处】的字节",
			})
			continue
		}
		embSHA := embCopySHA256(filepath.Join(embedDir, name))
		if embSHA == "" {
			out = append(out, embCopyViolation{
				File:   name,
				Reason: "★ 读不出 embeddata 副本的字节 ⇒ 不可判定",
			})
			continue
		}
		matched := false
		var canonSHAs []string
		for _, p := range paths {
			s := embCopySHA256(p)
			canonSHAs = append(canonSHAs, fmt.Sprintf("%s=%s", p, s[:12]))
			if s != "" && s == embSHA {
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, embCopyViolation{
				File: name,
				Reason: fmt.Sprintf("★ embeddata 副本与 canonical 【逐字不一致】⇒ "+
					"⇒ 新装通道与升级通道会跑【不同的字节】⇒ embed sha=%s / canonical %s",
					embSHA[:12], strings.Join(canonSHAs, " ")),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// embCopyJudgedCount 数「被判据判到的文件数」（★ 有迁移号前缀的那些）。
func embCopyJudgedCount(embedDir string) int {
	entries, err := os.ReadDir(embedDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && embCopyIsMigrationNumbered(e.Name()) {
			n++
		}
	}
	return n
}

// ─────────────────────── 判据①：全部副本逐字一致 ───────────────────────

// TestEmbeddataCopiesAreByteIdenticalToCanonical —— 本门的主判据。
func TestEmbeddataCopiesAreByteIdenticalToCanonical(t *testing.T) {
	canon := embCopyIndexCanonical(embCopyCanonRel)
	violations := embCopyJudge(embCopyEmbedRel, canon)
	if len(violations) > 0 {
		for _, v := range violations {
			t.Errorf("embeddata 副本一致性门：%s", v)
		}
		t.Errorf("★ embeddata 副本一致性门：%d 条违例", len(violations))
	}

	// ★★ 反向对照：★ 判据面【不得退化】。
	//   ★ 若哪天 embeddata 目录被清空/改名 ⇒ 上面的门会「零违例恒绿」
	//   ⇒ ★ 必须钉住「判到了多少个文件」。
	judged := embCopyJudgedCount(embCopyEmbedRel)
	if judged < embCopyMinJudged {
		t.Errorf("★ 判据面退化：★ 只判到 %d 个文件（★ 下限 %d）⇒ ⇒ "+
			"★ 门在【近乎空的面】上恒绿 ⇒ ★ 判据已失效",
			judged, embCopyMinJudged)
	}
	t.Logf("★ embeddata 副本一致性门：判据面 %d 个文件 / ★ 违例 0", judged)
}

// ─────────────────────── 判据②：installer-only 声明清单双向核对 ───────────────────────

// TestEmbeddataInstallerOnlyAssetsAreDeclared —— 声明清单与目录【双向】一致。
func TestEmbeddataInstallerOnlyAssetsAreDeclared(t *testing.T) {
	entries, err := os.ReadDir(embCopyEmbedRel)
	if err != nil {
		t.Fatalf("读 embeddata 目录：%v", err)
	}
	actual := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() && !embCopyIsMigrationNumbered(e.Name()) {
			actual[e.Name()] = true
		}
	}

	for name, reason := range embCopyInstallerOnly {
		if reason == "" {
			t.Errorf("★ 声明清单里 %s 的理由是空的 ⇒ ⇒ 不许留空理由", name)
		}
		if !actual[name] {
			t.Errorf("★ 声明清单里的 %s 在 embeddata 目录里【已不存在】⇒ ⇒ "+
				"★ 冗余声明 ⇒ 请确认那份资产真的不见了再删条目", name)
		}
	}
	for name := range actual {
		if _, ok := embCopyInstallerOnly[name]; !ok {
			t.Errorf("★ embeddata 目录里有未声明的 installer-only 资产 %s ⇒ ⇒ "+
				"★ 它没有 canonical、★ 也不在声明清单里 ⇒ 请补声明并写理由", name)
		}
	}
	t.Logf("★ installer-only 声明清单：%d 条 / ★ 目录里无前缀文件 %d 个",
		len(embCopyInstallerOnly), len(actual))
}

// ─────────────────────── 牙：合成夹具（★ 仓内真文件零改动） ───────────────────────

// TestEmbeddataCopyIdentityGateHasTeeth —— ★ 变异臂：★ 判据必须能判红。
//
// ★★ 为什么必须做这一条：★ 判据是「逐字相同」这种**弱形式**的断言
//
//	⇒ ★ 若实现写错（例如比较了错误的路径、★ 或把 sha 比成了空串）
//	⇒ ★ **主判据会恒绿** ⇒ ★ 而门绿着、★ 却什么也没拦住。
//
// ⇒ ⇒ ★ 合成夹具在**临时目录**上用**同一份** `embCopyJudge` 跑。
func TestEmbeddataCopyIdentityGateHasTeeth(t *testing.T) {
	tmp := t.TempDir()
	embedDir := filepath.Join(tmp, "embeddata")
	canonRoot := filepath.Join(tmp, "canonical")
	if err := os.MkdirAll(embedDir, 0o755); err != nil {
		t.Fatalf("建 embeddata 目录：%v", err)
	}
	if err := os.MkdirAll(filepath.Join(canonRoot, "up"), 0o755); err != nil {
		t.Fatalf("建 canonical 目录：%v", err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("写 %s：%v", path, err)
		}
	}

	// ① 逐字相同 ⇒ 不该红。
	write(filepath.Join(canonRoot, "900_same.sql"), "-- same\n")
	write(filepath.Join(embedDir, "900_same.sql"), "-- same\n")

	// ② 逐字不同 ⇒ 必须红。
	write(filepath.Join(canonRoot, "901_diff.sql"), "-- canonical\n")
	write(filepath.Join(embedDir, "901_diff.sql"), "-- embeddata\n")

	// ③ canonical 只在【子目录】里 ⇒ 递归查找必须找到它 ⇒ 不该红。
	write(filepath.Join(canonRoot, "up", "902_in_sub.sql"), "-- sub\n")
	write(filepath.Join(embedDir, "902_in_sub.sql"), "-- sub\n")

	// ④ canonical 完全没有 ⇒ 必须红。
	write(filepath.Join(embedDir, "903_missing.sql"), "-- orphan\n")

	// ⑤ 无前缀且未声明 ⇒ 必须红。
	write(filepath.Join(embedDir, "zz_undeclared_bootstrap.sql"), "-- bootstrap\n")

	canon := embCopyIndexCanonical(canonRoot)
	violations := embCopyJudge(embedDir, canon)

	got := map[string]bool{}
	for _, v := range violations {
		got[v.File] = true
	}
	for _, name := range []string{"901_diff.sql", "903_missing.sql", "zz_undeclared_bootstrap.sql"} {
		if !got[name] {
			t.Errorf("★ 变异臂失效：%s 应当被判红、★ 但门没报 ⇒ ⇒ 判据没有牙", name)
		}
	}
	for _, name := range []string{"900_same.sql", "902_in_sub.sql"} {
		if got[name] {
			t.Errorf("★ 假阳性：%s 逐字相同（★ 902 的 canonical 还在子目录里）⇒ ★ 不该红", name)
		}
	}
	if len(violations) != 3 {
		t.Errorf("★ 变异臂违例数 = %d（★ 期望 3：★ 901 / 903 / zz）⇒ ⇒ 判据行为与设计不符：%v",
			len(violations), violations)
	}
	t.Logf("★ 变异臂：%d 条违例（★ 逐字不同 / canonical 缺失 / 未声明的 installer-only）", len(violations))
}
