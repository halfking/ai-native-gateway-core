package ursm_test

// tsv_hollowout_safety.go —— 掏空法门的**落盘安全**配套（2026-10-24 12h 审计轮问题②）。
//
// # 为什么需要这个文件
//
// 掏空法门（tsv_hollowout_gate_test.go）要把被跟踪的 `.tsv` 登记册**真的掏空**，
// 跑一遍读方，看它红不红，然后还原。
//
// ★ **本轮审计抓到的高危缺陷**：原实现是
//
//	orig, _ := os.ReadFile(abs)          // 原文只在内存里
//	os.WriteFile(abs, hollowed, 0o644)  // 直接覆盖**仓内被跟踪的真实路径**
//	defer os.WriteFile(abs, orig, 0o644) // 还原靠 defer
//
// 而 `testing` 的超时是 **alarm goroutine 里的 panic**
// ⇒ ⇒ **其它 goroutine 的 `defer` 不会执行** ⇒ ⇒ 进程被 SIGKILL 时
// ⇒ ⇒ **被跟踪的台账文件静默留空**。
//
// ★ **本轮实测复现**：门运行期间
// `scripts/ursmcheck/selfcreated_cleanup_quality_ledger.tsv` 被当场掏空（−21 行）；
// 本机实跑 **293.047s**，而 `verify.sh:143` 的 `go test ./... -timeout=300s`
// 只剩 **7 秒余量** ⇒ 负载一高就超时 ⇒ ⇒ 台账文件留空。
//
// # 本文件的修法（三层，自愈优先）
//
//	① **备份落盘**：掏空前把原文写到 `<abs>.hollowout-backup`（与台账同目录）。
//	   ⇒ 即使进程被 SIGKILL，**原文仍在盘上** ⇒ 可还原。
//	② **自愈预检**：门开跑前先扫一遍「有没有遗留的备份」⇒ 有就**先还原再开跑**
//	   ⇒ ⇒ **上一次被杀留下的空洞，会被下一次跑自动修好**。
//	③ **信号处理**：SIGINT / SIGTERM 到达时立刻还原。
//
// ★ 设计要点：**还原的依据是「盘上的备份」，不是「内存里的原文」**
// ⇒ ⇒ 这正是原实现最缺的那一层（内存副本随进程一起没了）。
//
// ★ 另外澄清一条被误报的缺陷：`os.WriteFile` 的 perm 参数**只对新建文件生效**
// （`OpenFile(..., O_CREATE, perm)`）⇒ ⇒ 对**已存在**的文件反复 WriteFile
// **不会重置权限** ⇒ 原实现的「权限被重置」不成立，本文件不修它。

import (
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

// hollowBackupSuffix 是备份文件后缀。★ 放在台账同目录 ⇒ 与台账同生共死
// （仓内任何一次 clone / checkout 都不会把它带走，备份也就不会失效）。
const hollowBackupSuffix = ".hollowout-backup"

// hollowBackupPath 返回某台账的备份路径。
func hollowBackupPath(abs string) string { return abs + hollowBackupSuffix }

// restoreHollowBackup 用备份还原一个被掏空的台账。
//
// ★ 返回值：
//   - nil：还原成功（或本来就没有备份 ⇒ 无事可做）
//   - error：还原失败（调用方必须**让门红**，不能吞掉）
//
// ★ 注意：还原时**不覆盖权限**。`os.WriteFile` 对已存在文件不改权限，
// 而掏空版的 WriteFile 也没改 ⇒ ⇒ 权限天然保持原样，无需额外处理。
func restoreHollowBackup(abs string) error {
	backup := hollowBackupPath(abs)
	orig, err := os.ReadFile(backup)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 没有备份 ⇒ 无事可做
		}
		return err
	}
	if err := os.WriteFile(abs, orig, 0o644); err != nil {
		return err
	}
	return os.Remove(backup)
}

// healHollowLeftovers 是【自愈预检】：把上一次被 SIGKILL 留下的掏空台账全部还原。
//
// ★ 触发时机：**门开跑之前**（而不是只在失败时）⇒ ⇒
// 「上一次跑崩了留下的空洞」会被**下一次跑自动修好**，
// 而不是永远留在工作树里等人手工 `git checkout`。
//
// ★ 返回值：还原条数 + 错误（任一还原失败 ⇒ 门必须红）。
func healHollowLeftovers(root string) (int, error) {
	// ★★ R87 轮收尾：★ 自愈预检**自己也会写盘**（把遗留的掏空台账还原回去）
	// ⇒ ⇒ 它同样落在「变异工作树」的窗口里 ⇒ ⇒ 也要拿【整仓掏空锁】。
	// ★ 不拿锁的后果：★ 预检会把**另一个正在跑的掏空版的备份**当成「遗留」
	// ⇒ ⇒ 把它还原掉 ⇒ ⇒ 那个跑在自己的掏空窗口里读到的是【原文】⇒ ⇒ **误判 still-green**。
	releaseLock, _, err := lockHollowoutShared(root, hollowoutLockWait)
	if err != nil {
		return 0, err
	}
	defer releaseLock()

	rels, err := hollowAllTrackedTsv(root)
	if err != nil {
		return 0, err
	}
	healed := 0
	for _, rel := range rels {
		abs := filepath.Join(root, rel)
		if _, err := os.Stat(hollowBackupPath(abs)); err != nil {
			continue // 没有遗留备份
		}
		if err := restoreHollowBackup(abs); err != nil {
			return healed, err
		}
		healed++
	}
	return healed, nil
}

// installHollowSignalGuard 安装 SIGINT / SIGTERM 处理器：
// 收到信号时**立刻**还原当前正在掏空的台账，然后退出。
//
// ★ 为什么要有：SIGINT（Ctrl-C）与 SIGTERM（CI / 外部超时器）**不会**让
// `testing` 跑 defer ⇒ ⇒ 必须自己处理。
//
// ★ 局限（诚实标注）：**SIGKILL 无法捕获** ⇒ 那一条只能靠「备份落盘 + 自愈预检」兜底。
func installHollowSignalGuard(abs string) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-ch:
			_ = restoreHollowBackup(abs)
			os.Exit(130)
		case <-done:
		}
	}()
	return func() {
		close(done)
		signal.Stop(ch)
	}
}

// TestHollowBackupSelfHealRestoresLeftover 是【自愈机制本身】的门。
//
// ★ 它证明的是「上一次被 SIGKILL 留下的掏空台账，会被下一次跑自动还原」——
// 这正是原实现缺的那一层（还原依据只在内存里，进程一死就没了）。
//
// ★ 变异：把 restoreHollowBackup 改成不写回 ⇒ 本门红。
func TestHollowBackupSelfHealRestoresLeftover(t *testing.T) {
	root := hollowRepoRoot(t)
	rels, err := hollowAllTrackedTsv(root)
	if err != nil {
		t.Fatalf("列仓内 .tsv 失败：%v", err)
	}
	if len(rels) == 0 {
		t.Fatalf("仓内没有 .tsv ⇒ 量具坏了")
	}

	// 拿一份**真实**台账做「模拟被杀」：先备份、再掏空（不还原），
	// 然后调用自愈逻辑，看它能不能还原回去。
	rel := rels[0]
	abs := filepath.Join(root, rel)
	orig, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", rel, err)
	}
	origMD5 := md5Of(orig)

	// 模拟一次「被 SIGKILL」：只写备份 + 写掏空版，**不还原**。
	hollowed, dropped, _ := hollowBytes(orig, 0)
	if dropped == 0 {
		t.Skipf("%s 掏空是空操作 ⇒ 本门不适用", rel)
	}
	if err := os.WriteFile(hollowBackupPath(abs), orig, 0o644); err != nil {
		t.Fatalf("写备份失败：%v", err)
	}
	if err := os.WriteFile(abs, hollowed, 0o644); err != nil {
		t.Fatalf("写掏空版失败：%v", err)
	}
	// 此时文件应当是「空的」且备份存在 ⇒ 这正是被杀后的现场。
	if md5Of(hollowMustReadBytes(t, abs)) == origMD5 {
		t.Fatalf("%s：模拟掏空后 md5 没变 ⇒ 模拟无效", rel)
	}
	if _, err := os.Stat(hollowBackupPath(abs)); err != nil {
		t.Fatalf("备份不在盘上 ⇒ 模拟无效：%v", err)
	}

	// 自愈。
	healed, err := healHollowLeftovers(root)
	if err != nil {
		t.Fatalf("自愈失败：%v", err)
	}
	if healed < 1 {
		t.Fatalf("自愈没有还原任何文件（healed=%d）⇒ 机制没生效", healed)
	}
	back := hollowMustReadBytes(t, abs)
	if got := md5Of(back); got != origMD5 {
		t.Errorf("自愈后 md5 不符：期望 %s，实际 %s", origMD5, got)
	}
	if _, err := os.Stat(hollowBackupPath(abs)); !os.IsNotExist(err) {
		t.Errorf("自愈后备份文件应被删除，仍在：%s", hollowBackupPath(abs))
	}
}

// hollowMustReadBytes 读文件，失败即 t.Fatal。
//
// ★ 刻意不叫 mustRead：仓内 scripts/ursmcheck 包里已有同名 helper
// （destructive_target_guard_test.go），签名不同 ⇒ 不能重名。
func hollowMustReadBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", path, err)
	}
	return b
}
