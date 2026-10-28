package ursm_test

// hollowout_bin_cleanup_test.go —— 【掏空门测试二进制临时目录】的**清理**（★ 2026-10-28 R87 轮收尾）。
//
// # 为什么需要这个文件
//
// 掏空法门为了让读方跑在「**被改瞎的副本**」上（★ 编译期 overlay），
// 需要**现编译**被读包的测试二进制 ⇒ ⇒ 用 `os.MkdirTemp("", "hollowout-bin-")` 建临时目录。
//
// ★ ★★ **原实现只创建、从不删除** ⇒ ⇒ **每次跑都泄漏一批目录**：
// ★ 一个二进制 ~74 MB（★ `admin` / `bg` 这种大包更大）⇒ ⇒ 35 条目 × 2 个变体（★ 基线版 ＋ 掏空版）
// ⇒ ⇒ ⇒ **单次跑就泄漏 GB 级** ⇒ ⇒ ⇒ ★★ 累积下来 ⇒ ⇒ ★★ **把 macOS 的每用户 tmp 配额撑爆**
// ⇒ ⇒ ⇒ ⇒ 实测本机 `/private/var/folders/.../T` 里堆了 **224 个 `hollowout-bin-*` ＝ 8.6 GB**
// ⇒ ⇒ ⇒ ⇒ 最早的是 **两天前** ⇒ ⇒ ⇒ ⇒ ★★ **不是本轮才有的** ⇒ ⇒ ⇒ ⇒ **是历次跑都在漏**。
//
// ★ ★★ **它带来的真实故障（★ 本轮实测）**：★ tmp 配额耗尽 ⇒ ⇒ `os.WriteFile` 报
// ★ **`no space left on device`** ⇒ ⇒ ⇒ 门里几个**要写临时文件**的门当场红（★ `git init` 写不了 `.git/config.lock`
// ⇒ ⇒ ⇒ 写不了变异探针 ⇒ ⇒ ⇒ 脚本跑不到查询 ⇒ ⇒ ⇒ trace 文件不存在）
// ⇒ ⇒ ⇒ ★★ **红的原因不指向真正的原因** ⇒ ⇒ ⇒ ★★ 而真正的原因是「**磁盘**」，不是「门有洞」。
//
// # 修法
//
// ★ ① 每次建目录时**登记**到包级注册表；
// ★ ② `TestMain` 在**所有测试跑完之后**把注册表里的目录**全部删掉**。
//
// ★ 为什么用 `TestMain` 而不是 `t.Cleanup`：★ 缓存是**包级**的（★ `hollowBinCache`），
// ⇒ ⇒ 同一次跑里多个条目**共用**同一批二进制 ⇒ ⇒ ⇒ 每个条目结束时删会**把后面的条目也删掉**
// ⇒ ⇒ ⇒ 只能「**整个进程结束时**」统一删。
//
// ★★ 为什么要**每次新建**而不改成「跨进程复用的固定缓存目录」：
// ★ 复用会引入「**陈旧缓存**」问题（★ 源码改了但缓存没失效 ⇒ ⇒ 读方跑的是旧二进制 ⇒ ⇒ **假绿**）
// ⇒ ⇒ ⇒ 而本线的判据宁可慢也不能假绿 ⇒ ⇒ ⇒ **保持每次新建 ＋ 跑完删除**。

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

// hollowBinTmpDirs 登记本进程创建过的【掏空门测试二进制临时目录】。
var (
	hollowBinTmpMu   sync.Mutex
	hollowBinTmpDirs []string
)

// registerHollowBinTmpDir 登记一个临时目录（★ 供 TestMain 收尾时删除）。
func registerHollowBinTmpDir(dir string) {
	hollowBinTmpMu.Lock()
	defer hollowBinTmpMu.Unlock()
	hollowBinTmpDirs = append(hollowBinTmpDirs, dir)
}

// cleanupHollowBinTmpDirs 删掉所有登记过的临时目录（★ 返回删掉几个 ＋ 第一个错误）。
func cleanupHollowBinTmpDirs() (int, error) {
	hollowBinTmpMu.Lock()
	dirs := hollowBinTmpDirs
	hollowBinTmpDirs = nil
	hollowBinTmpMu.Unlock()

	n := 0
	var firstErr error
	for _, d := range dirs {
		if err := os.RemoveAll(d); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("清理 %s 失败：%w", d, err)
			}
			continue
		}
		n++
	}
	return n, firstErr
}

// TestMain 是本包（★ `ursm_test`）的**唯一**入口钩子。
//
// ★ 它只做一件事：★ 跑完所有测试后**清掉掏空门留下的测试二进制临时目录**。
// ★★ **刻意不做别的**（★ 不改退出码 ＋ 不吞错误）⇒ ⇒ 清理失败只**打印**，不改变测试结果
// ⇒ ⇒ ★★ 清理是**卫生问题**，不该让它变成**判据问题**。
func TestMain(m *testing.M) {
	// ★★★ r1046（2026-10-28）：★ 跑之前先给掏空门的【专用构建缓存】**把尺寸按住**。
	//
	// ★★ **为什么**：★ 掏空门每在一个**新树状态**上跑一次 ⇒ 就会往缓存里塞 ~1.3 GB
	// ★★ ⇒ ⇒ 那些条目随树变化而**永远不再被命中** ⇒ ⇒ 事实上的垃圾 ⇒ ⇒ 必须有上限。
	// ★★ ⇒ ⇒ 上限之内不动（★ 保热缓存 ⇒ 不让门一直冷编译）；★ 超过就**整个删掉重建**。
	//
	// ★★ **剪枝失败不改变测试结果**（★ 只打印）⇒ ⇒ 与本文件「清理是卫生问题，
	// ★★ 不该让它变成判据问题」那条纪律**同款**。
	if root, err := hollowRepoRootNoT(); err == nil {
		dir := hollowGoCacheDir(root)
		if pruned, perr := pruneHollowGoCache(dir, hollowGoCacheMaxBytes); perr != nil {
			fmt.Fprintf(os.Stderr, "★ 剪枝掏空门专用构建缓存失败（★ 不影响测试结论）：%v\n", perr)
		} else if pruned {
			fmt.Fprintf(os.Stderr, "掏空门专用构建缓存超上限 ⇒ 已整个删掉重建（★ 这轮是冷编译）\n")
		}
	}
	code := m.Run()
	n, err := cleanupHollowBinTmpDirs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "★ 清理掏空门测试二进制临时目录失败（★ 不影响测试结论）：%v\n", err)
	}
	if n > 0 {
		fmt.Fprintf(os.Stderr, "掏空门清理：删掉 %d 个测试二进制临时目录\n", n)
	}
	os.Exit(code)
}
