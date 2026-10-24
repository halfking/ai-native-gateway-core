package ursm_test

// audit_guard_registration_test.go —— ★ **系统性门**：门写了却没人跑 ⇒ 在 CI 上红。
//
// # 这道门补的是哪块缺口（★ 本轮实测撞到的）
//
// 仓库自己的 Makefile 注释写着：
//
//	「加了个守卫但没人跑」是这类体系最常见的腐化方式，且**悄无声息**：
//	新守卫的测试在本地跑是绿的，CI 也是绿的，只是它从来没在强制路径上跑过。
//
// ★ 本轮真的撞上了：magpie 对齐轮新增的**契约门**分别落在
// `internal/rewrite` / `internal/routeorder` / `internal/requestobserver` /
// `domains/credential` / `domains/authentication` —— ★ **五个包都不在
// `GUARD_PACKAGES` 里** ⇒ CI 的 `make guards` 从不执行它们 ⇒
// 「门是绿的」这件事在 CI 上是**空转**。
//
// ★ 而既有机制抓不到它：`scripts/checks/guards-sync.sh` 的发现规则是
// 「`internal/` 下**目录名以 `guard` 结尾**的包」⇒ 我这几个包既不以 `guard`
// 结尾，又不在 `internal/` 下 ⇒ **既不被检查，也不被执行**。
//
// ⇒ 本门换一个**显式**判据：**门文件自己声明**（文件头一行 `AUDIT-GUARD:`）
// ⇒ 声明了 ⇒ **它所在的包必须被登记进 Makefile 的 `GUARD_PACKAGES` 或
// `GUARD_PACKAGES_SLOW`** ⇒ 否则 CI 红。
//
// ★ 为什么用「显式标记」而不是「按文件命名/按目录位置自动发现」：
// 仓内已有 **70+** 个 `*_contract_test.go` 散落在几十个包（admin / bg / db /
// sql/... ），它们**本来就不在 CI 快门里**（跑的是别的机制）⇒
// 若按命名自动发现，这道门会要求把几十个包全拖进 CI ⇒ 那是**另一个**决策，
// 不能由本门偷偷做。⇒ ⇒ **显式标记 = 「我保证这个门在 CI 上跑」**，
// 由写门的人**主动声明**，本门只校验这份声明与 Makefile **一致**。
//
// # 三条判据
//
//	G1 仓内**带 `AUDIT-GUARD` 标记的门文件**至少有一个（否则本门恒绿 = 形同虚设）
//	G2 ★ 每个带标记的门文件，其**所在包目录**必须出现在 Makefile 的
//	   `GUARD_PACKAGES` ∪ `GUARD_PACKAGES_SLOW` 里
//	G3 Makefile 里登记的**每一个**条目都必须真实存在于磁盘上
//	   （反向检查：登记了不存在的包 ⇒ 登记本身是假的）

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// auditGuardMarker 是门文件的**显式声明**标记。
const auditGuardMarker = "AUDIT-GUARD:"

// auditGuardRepoRoot 返回仓根。
func auditGuardRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("解析仓根失败（需要 git 在 PATH）：%v", err)
	}
	return strings.TrimSpace(string(out))
}

// markedGuardFiles 返回仓内所有**带 AUDIT-GUARD 标记**的门文件（相对仓根，已排序）。
func markedGuardFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// 跳过 vendor 与 .git（第三方代码不是本仓的门）。
			name := d.Name()
			if name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if !strings.Contains(string(b), auditGuardMarker) {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("扫门文件失败：%v", err)
	}
	return out
}

// makefileGuardPackages 返回 Makefile 的 GUARD_PACKAGES ∪ GUARD_PACKAGES_SLOW。
//
// ★ 解析口径与 `scripts/checks/guards-sync.sh` **保持一致**（单行 `:=` 定义），
// 但**不复用**它的实现（那是 shell）⇒ 两边各自独立 ⇒ 一边坏了另一边还能报。
var makefileGuardPackagesRE = regexp.MustCompile(`(?m)^GUARD_PACKAGES(_SLOW)?\s*:?=\s*(.*)$`)

func makefileGuardPackages(t *testing.T, root string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("读 Makefile 失败：%v", err)
	}
	out := map[string]bool{}
	for _, m := range makefileGuardPackagesRE.FindAllStringSubmatch(string(b), -1) {
		for _, tok := range strings.Fields(m[2]) {
			tok = strings.TrimSuffix(tok, "\\")
			if !strings.HasPrefix(tok, "./") {
				continue
			}
			out[tok] = true
		}
	}
	return out
}

// TestAuditGuardMarkedFilesExist 是【量具自检】：门文件确实被扫到了。
//
// ★ 没有这一条，G1/G2 会在「一个标记都没扫到」时**恒绿** ⇒ 形同虚设。
func TestAuditGuardMarkedFilesExist(t *testing.T) {
	root := auditGuardRepoRoot(t)
	files := markedGuardFiles(t, root)
	if len(files) == 0 {
		t.Fatalf("仓内没有任何带 %q 标记的门文件 ⇒ 本门恒绿 ⇒ 判据失效", auditGuardMarker)
	}
	t.Logf("扫到 %d 个带标记的门文件：%s", len(files), strings.Join(files, ", "))
}

// TestAuditGuardMarkedPackagesAreRegistered G2：
// ★ 每个带标记的门文件，其**所在包**必须被登记进 Makefile 的门禁清单。
func TestAuditGuardMarkedPackagesAreRegistered(t *testing.T) {
	root := auditGuardRepoRoot(t)
	files := markedGuardFiles(t, root)
	if len(files) == 0 {
		t.Fatalf("没有带标记的门文件 ⇒ 判据失效")
	}
	registered := makefileGuardPackages(t, root)

	// 同一个包可能有多个门文件 ⇒ 只报一次。
	seen := map[string]bool{}
	for _, f := range files {
		dir := "./" + filepath.ToSlash(filepath.Dir(f))
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if !registered[dir] {
			t.Errorf("门文件 %s 声明了 %q，但它的包 %s **不在** Makefile 的 "+
				"GUARD_PACKAGES / GUARD_PACKAGES_SLOW 里 ⇒ CI 从不执行它 ⇒ "+
				"「门是绿的」在 CI 上是空转。\n"+
				"★ 修法：把 %s 加进 Makefile 的 GUARD_PACKAGES（快门，120s/包）"+
				"或 GUARD_PACKAGES_SLOW（慢门）。",
				f, auditGuardMarker, dir, dir)
		}
	}
}

// TestAuditGuardRegisteredPackagesExistOnDisk G3（反向检查）：
// Makefile 里登记的**每一个**包都必须真实存在于磁盘上。
//
// ★ 没有这一条，「登记了一个不存在的包」会让 CI 以为门跑过了，
// 而实际上 `go test ./不存在的包` 会**报错**（或被某些 target 吞掉）。
func TestAuditGuardRegisteredPackagesExistOnDisk(t *testing.T) {
	root := auditGuardRepoRoot(t)
	registered := makefileGuardPackages(t, root)
	if len(registered) == 0 {
		t.Fatal("从 Makefile 解析出的门禁包为空 ⇒ 解析器失效")
	}
	for pkg := range registered {
		dir := filepath.Join(root, strings.TrimPrefix(pkg, "./"))
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Errorf("Makefile 登记的门禁包 %s 在磁盘上不存在（%v）⇒ 登记本身是假的", pkg, err)
		}
	}
}
