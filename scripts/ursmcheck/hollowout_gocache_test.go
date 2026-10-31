package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。

// hollowout_gocache_test.go —— r1047 轮（2026-10-28）：掏空门的【两道泄漏】合到**一道门**里
//
// # ★★ 本文件为什么同时管【两半】（★ r1047 轮：★ 「同一个问题的两半」合并）★★
//
// ★★★ **两半是同一个问题的两面**：★★★
//
// ★ ① **tmp 目录那一半**（★ r1045 轮修的）：★ 掏空门为了让读方跑在「被改瞎的副本」上
// ★★★ ⇒ ⇒ 它**现编译**被读包的测试二进制 ⇒ ⇒ 临时目录「**只创建不删除**」⇒ ⇒
// ★★★ **每次跑泄漏一批目录**（★ 实测本机 `/private/var/folders/.../T` 里堆了 **224 个
// ★★★ `hollowout-bin-*` ＝ 8.6 GB**）⇒ ⇒ ⇒ ★★ **把 macOS 的每用户 tmp 配额撑爆**
// ★★★ ⇒ ⇒ ⇒ ★★ 而**配额耗尽 ⇒ ⇒ `os.WriteFile` 报 `no space left on device`**
// ★★★ ⇒ ⇒ ⇒ ★★ ⇒ **红的原因不指向真正的原因**（★ 真正的原因是磁盘，不是门有洞）。
//
// ★ ② **GOCACHE 那一半**（★ r1046 轮修的）：★ 子构建写进**用户那个全局缓存**⇒ ⇒
// ★★★ **每次新树 +1.3 GB** ⇒ ⇒ ⇒ ★★ 同款「红的原因不指向真正的原因」。
//
// ★★★★ **它们是同一件事的两面：★ 「门自己的产物落在磁盘上，而没人回收它」★★**
// ★★★★ ⇒ ⇒ ★★ ⇒ **既然是同一件事，就该由**一道门**管** ⇒ ⇒ ★★ ⇒ **分在两个文件里
// ★★★★ ⇒ ⇒ ⇒ ★★ ⇒ 【责任分散 ⇒ ⇒ ⇒ 而且【tmp 那一半压根没有判据】—— ★★ ★★ r1047
// ★★★★ ⇒ ⇒ ⇒ ★★ ⇒ 轮实测：`hollowout_bin_cleanup_test.go` 里**一个 `Test` 函数都没有**
// ★★★★ ⇒ ⇒ ⇒ ★★ ⇒ **那半边【修完了但没人验】** ⇒ ⇒ ★★ ⇒ ⇒ **「修了」不等于「验了」。★★★**
//
// # ② GOCACHE 那一半的来龙去脉（★ r1046 轮，★ 实测，2026-10-28）
//
// # 为什么需要这个文件（★ 实测，2026-10-28）
//
// ★★ **背景**：★ 掏空法门要让读方跑在「**被改瞎的副本**」上 ⇒ ⇒ 它会**现编译**被读包的
// ★★ 测试二进制 ⇒ ⇒ 编译发生在**子进程**里 ⇒ ⇒ 子进程用的是**用户那个全局 `GOCACHE`**。
//
// ★★★ **实测的泄漏量**（★ 2026-10-28，★ 本机，★ 同一棵树上跑两遍）：
//
//	第 1 遍（冷）：★ GOCACHE **+1281 MB**（★ 2.3 GB → 3.5 GB）★ 耗时 213.35s
//	第 2 遍（同一棵树、热）：★ GOCACHE **+0 MB** ★ 耗时 153.99s
//
// ★★★ **这个读数说明的不是「每次跑都漏」，而是「【树变一次就漏一批】」** ——
// ★★★ 因为掏空门在跑的过程中**改写仓内 `.tsv` 的内容**（★ `//go:embed` 在**编译期**
// ★★★ 把文件内容嵌进二进制 ⇒ ⇒ 内容一变 ⇒ ⇒ **构建的 action ID 就变**）
// ★★★ ⇒ ⇒ 树一旦变（★ 改一行代码、合并一次、换一个分支……）⇒ ⇒ 门就要为**每个读方包**
// ★★★ **重新编译出全新的一套二进制** ⇒ ⇒ 旧的那些**永远不会再被命中** ⇒ ⇒ 它们是
// ★★★ **事实上的垃圾**，★ 却躺在用户那个「跨项目、跨天、Go 只按 5 天 trim」的全局缓存里。
//
// ★★★ **实测规模**：★ 本机全局 `GOCACHE` 一度到 **72 GB** ⇒ ⇒ 磁盘 100% 满
// ★★★ ⇒ ⇒ 全量 `go test ./...` **编译阶段**就报 `no space left on device`
// ★★★ ⇒ ⇒ ★★ **红的原因不指向真正的原因**（★ 真正的原因是磁盘，不是门有洞）。
//
// ★★ ★★ 这正是 `hollowout_bin_cleanup_test.go` 里**已经写下**的那句话
// ★★ （★ 原文：「★ 红的原因不指向真正的原因（★ 真正的原因是磁盘，不是门有洞）」）——
// ★★ ★★ 但那一轮只修了 **tmp 那一半**（★ 测试二进制临时目录「只创建不删除」⇒ ⇒ 泄漏 8.6 GB）
// ★★ ⇒ ⇒ **GOCACHE 那一半一直没修** ⇒ ⇒ **本文件补的就是那一半**。
//
// # 修法（★ 两件事，★ 合起来才闭环）
//
// ① **隔离**：★ 掏空门的子构建改用**专用** `GOCACHE`（★ `<仓根>/.build-local/.gocache-hollowout`）
//    ⇒ ⇒ **用户的全局缓存不再被掏空门污染**（★ 这才是核心收益）；
// ② **把住尺寸**：★ 专用缓存**自己**也是会涨的（★ 树变一次涨一批）⇒ ⇒ 给它一个
//    **尺寸上限**，超了**整个删掉重建**（★ 反正那些条目在下一次树变时也全部失效）。
//
// ★★ **为什么专用缓存不会引入「陈旧缓存」**：★ Go 的构建缓存是**内容寻址**的
// ★★ （★ action ID = 全部输入内容 + 构建配置的哈希）⇒ ⇒ ★ **它不可能返回陈旧的产物**
// ★★ ⇒ ⇒ 与 `hollowout_bin_cleanup_test.go` 里担心的「跨进程复用二进制路径会陈旧」
// ★★ ⇒ ⇒ 是**两件不同的事**（★ 那一轮担心的是**他们自己缓存的二进制路径**，
// ★★ 不是 Go 的 GOCACHE）⇒ ⇒ ★★ **隔离不牺牲任何正确性**。
//
// ★★ **为什么专用缓存放在仓内 `.build-local/`**：★ ① 它**已经在 `.gitignore` 里**
// ★★ （★ `.gitignore:42` ⇒ `.build-local/`）⇒ ⇒ **绝不会被提交进仓库**；
// ★★ ② 它跟着仓走 ⇒ ⇒ `git clean -xfd` 能顺手回收它；
// ★★ ③ 它是**仓库内**的路径 ⇒ ⇒ 与 `scripts/deploy-seamless.sh` 等既有
// ★★ `.build-local/.gocache` 的约定**同款**。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	// hollowGoCacheRelDir 是掏空门专用构建缓存目录（★ 相对仓根，★ **必须在 `.build-local/` 下**）。
	//
	// ★★ 为什么必须在 `.build-local/` 下：★ 那一层**已经在 `.gitignore` 里**
	// ★★ （★ `.gitignore:42`）⇒ ⇒ 专用缓存**绝不会被提交进仓库**
	// ★★ ⇒ ⇒ 本文件里有一道门专门钉这条（★ 见 `TestHollowoutGoCacheDirIsNotTracked`）。
	hollowGoCacheRelDir = ".build-local/.gocache-hollowout"

	// hollowGoCacheMaxBytes 是专用缓存的**尺寸上限**（★ 超过就整个删掉重建）。
	//
	// ★ 依据：★ 实测掏空门每跑一次（★ 在**新树**上）往缓存里塞 **~1.3 GB**
	// ★ ⇒ ⇒ 上限取 **6 GB** ⇒ ⇒ 大约每 **4~5** 次新树触发一次清理
	// ★ ⇒ ⇒ 清理后下一轮是**冷编译**（★ 实测 213.35s vs 153.99s，★ 冷热差 ~1.4x）
	// ★ ⇒ ⇒ **代价可接受**，★ 而「磁盘被撑爆」的代价不可接受。
	hollowGoCacheMaxBytes int64 = 6 << 30

	// hollowGoCacheCapFloor 是【专用缓存上限】允许的**下界**（★ 1 GiB）。
	//
	// ★★ **为什么要有下界**：★ 上限若低于 1 GiB ⇒ ⇒ 掏空门每跑一次（新树状态）
	// ★★ 就必然超限 ⇒ ⇒ **每次都全清** ⇒ ⇒ 门**永远冷编译**（★ 实测冷 213.35s
	// ★★ vs 热 153.99s ⇒ ★ 冷热差 ~1.4x）⇒ ⇒ ⇒ 那个数写小了，门会恒定地慢。
	hollowGoCacheCapFloor int64 = 1 << 30

	// hollowGoCacheCapCeiling 是【专用缓存上限】允许的**上界**（★ 16 GiB）。
	//
	// ★★ **为什么是 16 GiB**：★ 实测每「新树状态」往缓存里塞 ~1.3 GB ⇒ ⇒
	// ★★ 16 GiB ≈ 12 个新树状态 ⇒ ⇒ 本机实测磁盘余量 75 GiB（★ 占用 92%）
	// ★★ ⇒ ⇒ 16 GiB 占余量 ~21% ⇒ ⇒ 仍在【一把可回收】的范围内（★ `.build-local/`
	// ★★ 在 `.gitignore` 里 ⇒ ⇒ `git clean -xfd` 一把回收）。
	//
	// ★★★ **上界的作用是【挡住「无界」这个形态】**（★ `math.MaxInt64` /
	// ★★★ `math.MaxInt32` / `1<<40` 这类）⇒ ⇒ ★★ **不是**把上限钉死成 6 GiB
	// ★★★ ⇒ ⇒ 那样会让「有意调参」也变红 ⇒ ⇒ 门变成「不许调参」的官僚门
	// ★★★ ⇒ ⇒ ★★ **调参的自由保留在带内**（★ 带外才是缺陷）。
	hollowGoCacheCapCeiling int64 = 16 << 30
)

var (
	hollowGoCacheOnce sync.Once
	hollowGoCachePath string
	hollowGoCacheErr  error
)

// hollowGoCacheDir 返回掏空门专用构建缓存目录的绝对路径（★ **不创建**）。
func hollowGoCacheDir(root string) string {
	return filepath.Join(root, filepath.FromSlash(hollowGoCacheRelDir))
}

// hollowGoCacheDirFor 返回专用缓存目录，★ 并确保它**存在**（★ 只试一次）。
//
// ★★ 建不出来时**不报错**：★ 返回错误 ⇒ ⇒ 调用方**回落**到环境里的 `GOCACHE`
// ★★ ⇒ ⇒ ★ **门照常跑**（★ 只是又回到「污染全局缓存」的老样子）
// ★★ ⇒ ⇒ ★★ **基础设施问题不该让判据红**（★ 仓库既有纪律：「★ 清理是卫生问题，
// ★★ 不该让它变成判据问题」⇒ ★★ 同款）。
func hollowGoCacheDirFor(root string) (string, error) {
	hollowGoCacheOnce.Do(func() {
		dir := hollowGoCacheDir(root)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			hollowGoCacheErr = fmt.Errorf("建掏空门专用构建缓存目录失败（%s）：%w", dir, err)
			return
		}
		hollowGoCachePath = dir
	})
	return hollowGoCachePath, hollowGoCacheErr
}

// hollowBuildEnv 返回掏空门【子进程】用的环境变量。
//
// ★★★ **核心**：★ 它在 `os.Environ()` **之后**追加 `GOCACHE=<专用目录>`
// ★★★ ⇒ ⇒ ★ 因为 Go 的 `exec` 语义是「**后出现的同名变量赢**」
// ★★★ ⇒ ⇒ ★ 所以**即便用户的环境里已经设了 `GOCACHE`**，子进程也会用专用目录。
//
// ★★ **注意**：★ `os.Environ()` 里原有的 `GOCACHE` **仍然留在切片里**
// ★★ ⇒ ⇒ ★ 所以读这个切片时**必须取最后一个匹配**（★ 见 `envValueLast`）
// ★★ ⇒ ⇒ ★★★ 这也是本文件那道门必须断言的东西（★ 断言「取最后一个」这件事本身）。
func hollowBuildEnv(root string) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"GIT_TERMINAL_PROMPT=0",
		// ★ 把 go 放进 PATH（★ shell 读方可能自己调 go）
		"PATH="+goBinDir()+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	if dir, err := hollowGoCacheDirFor(root); err == nil {
		// ★★ 覆盖（★ 靠「后出现的赢」）⇒ ⇒ 子进程用专用目录。
		env = append(env, "GOCACHE="+dir)
	} else {
		// ★ 回落：★ 不设 GOCACHE ⇒ ⇒ 子进程用环境里的那个 ⇒ ⇒ 门照常跑。
		fmt.Fprintf(os.Stderr, "★ 掏空门专用构建缓存不可用（★ 本次回落，用环境里的 GOCACHE）：%v\n", err)
	}
	return env
}

// envValueLast 取环境变量切片里**最后一个**匹配项的值（★ 空 ⇒ 没这个变量）。
//
// ★★★ **为什么必须取「最后一个」**：★ Go 的 `exec` 语义是「后出现的同名变量赢」
// ★★★ ⇒ ⇒ 而 `hollowBuildEnv` 是**追加** ⇒ ⇒ 原有的 `GOCACHE` 还在前面 ⇒ ⇒
// ★★★ 取第一个会拿到**用户那个全局缓存** ⇒ ⇒ ★★ 那样这道门就会「因为取错而恒绿」。
func envValueLast(env []string, key string) string {
	prefix := key + "="
	val := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			val = kv[len(prefix):]
		}
	}
	return val
}

// dirSizeBytes 递归求目录里**文件**的总字节数（★ 不计目录自身）。
func dirSizeBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// pruneHollowGoCache 若专用缓存目录超过上限 ⇒ **整个删掉**（★ 下一轮重建）。
//
// ★ 返回值：★ `pruned=true` 表示**确实删过**（★ 用来在日志里说清「为什么这轮是冷的」）。
//
// ★★ **为什么「整个删掉」而不是「删一部分」**：★ Go 的构建缓存**不支持按条目删除**
// ★★ ⇒ ⇒ 只能全清 ⇒ ⇒ ★★ 这也是「专用」的价值：★ **全清的代价被限制在一个
// ★★ 专用目录里**（★ 而不是用户的全局缓存 —— ★ 全清那个要重编整个仓的一切）。
func pruneHollowGoCache(dir string, maxBytes int64) (bool, error) {
	size, err := dirSizeBytes(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// ★ 目录本来就没有 ⇒ ★ 没什么可剪的 ⇒ ★★ **不是错误**。
			return false, nil
		}
		return false, fmt.Errorf("量掏空门专用构建缓存大小失败（%s）：%w", dir, err)
	}
	if size <= maxBytes {
		return false, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, fmt.Errorf("删掏空门专用构建缓存失败（%s）：%w", dir, err)
	}
	return true, nil
}

// hollowRepoRootNoT 是 `hollowRepoRoot` 的**无 testing.T 版**（★ `TestMain` 里没有 `t`）。
//
// ★ 解析失败**不致命**（★ 返回错误）⇒ ⇒ 调用方**跳过剪枝**（★ 不让基础设施问题让门红）。
func hollowRepoRootNoT() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("解析仓根失败（需要 git 在 PATH）：%w", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("解析仓根得到空串")
	}
	return root, nil
}

// ── ★★ tmp 目录那一半（★ r1045 轮修的 · ★ r1047 轮合到本文件）★★

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

// ── ③ 判据：★ tmp 目录那一半（★ r1047 轮补的 · ★★ 此前【压根没有判据】★★） ──

// TestHollowoutBinTmpDirsAreCleanedUp —— ★★ 掏空门留下的【测试二进制临时目录】
// ★ 必须**真的被删掉**（★ 否则每次跑都泄漏一批 ⇒ ⇒ ⇒ 撑爆用户 tmp 配额）。
//
// ★★★★ **为什么这道判据在 r1047 轮才出现**：★★★★ r1045 轮修好了这一半
// ★★★★ （★ 「只创建不删除」⇒ ⇒ 「登记 + 跑完统一删」）⇒ ⇒ ★★★★ **但那一轮
// ★★★★ 【没有给这一半配任何判据】** ⇒ ⇒ ★★★★ ⇒ 「修好了」**没人验** ⇒ ⇒ ★★★★
// ★★★★ ⇒ ⇒ 实测：★ r1047 轮合并前 `hollowout_bin_cleanup_test.go` 里**一个
// ★★★★ `Test` 函数都没有** ⇒ ⇒ ★★★★ ⇒ **这一半是【裸奔】的** ⇒ ⇒ ★★★★ ⇒
// ★★★★ ⇒ **「修了」不等于「验了」—— ★★ 这是本轮最朴素也最刺眼的一条。★★★★**
//
// ★ ★★ **为什么这条必须存在（★ 而不能只靠「肉眼读代码」）**：★ 清理失败只**打印**
// ★ ★★ 不改变测试结果（★ 「清理是卫生问题，不该让它变成判据问题」）⇒ ⇒ ★★ ⇒ **那条
// ★ ★★ 纪律是对的** ⇒ ⇒ ★★ ⇒ **但它同时意味着「清理坏了也没人会知道」** ⇒ ⇒ ★★ ⇒
// ★ ★★ ⇒ **所以要有一道【直接测那个纯函数】的判据**（★ `cleanupHollowBinTmpDirs`
// ★ ★★ 是纯函数 ⇒ ⇒ **可以直接测** ⇒ ⇒ ★★ ⇒ 而不必靠「跑一遍整个门」去观察副作用）。
func TestHollowoutBinTmpDirsAreCleanedUp(t *testing.T) {
	// ★ 登记两个**带内容**的目录（★ 带内容才测得出「是真删了」还是「只删了空壳」）。
	dirs := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		d := t.TempDir()
		if err := os.WriteFile(filepath.Join(d, "blob.bin"), make([]byte, 1024), 0o644); err != nil {
			t.Fatalf("写临时文件失败：%v", err)
		}
		registerHollowBinTmpDir(d)
		dirs = append(dirs, d)
	}
	// ★ 一个**没有登记**的目录（★ 清理**不许**碰它 ⇒ ⇒ 否则就成了「删半个 /tmp」）。
	unregistered := t.TempDir()

	n, err := cleanupHollowBinTmpDirs()
	if err != nil {
		t.Fatalf("★ 清理登记过的临时目录报错：%v", err)
	}
	if n != len(dirs) {
		t.Fatalf("★ 清理报告删了 %d 个目录，期望 %d 个", n, len(dirs))
	}
	for _, d := range dirs {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Fatalf("★ 登记过的临时目录 %s 还在（err=%v）⇒ ⇒ 每次跑都在泄漏一批目录", d, err)
		}
	}
	if _, err := os.Stat(unregistered); err != nil {
		t.Fatalf("★ 没登记过的目录 %s 被删了（err=%v）⇒ ⇒ 清理越界了", unregistered, err)
	}

	// ★ ★★ **注册表必须被清空**：★ 否则第二次清理会**重复删**同一批 ⇒ ⇒ ★★ ⇒
	// ★★ 而更糟的是：★ 若某个目录被**别的进程**重建了 ⇒ ⇒ ★★ ⇒ 第二次会把它删掉。
	n2, err := cleanupHollowBinTmpDirs()
	if err != nil {
		t.Fatalf("★ 第二次清理报错：%v", err)
	}
	if n2 != 0 {
		t.Fatalf("★ 第二次清理又删了 %d 个目录（★ ⇒ 注册表没被清空）", n2)
	}
}

// ── 判据（★ 七条，★ 必须成对） ──

// TestHollowoutBuildUsesIsolatedGoCache —— ★★ 正向断言：
// ★ 掏空门子进程用的 `GOCACHE` 必须是**专用目录**，且**不是**环境里那个。
func TestHollowoutBuildUsesIsolatedGoCache(t *testing.T) {
	root := hollowRepoRoot(t)

	// ★★ **反向守卫的另一半**：★ 先把环境的 `GOCACHE` 显式设成别的值
	// ★★ ⇒ ⇒ 子进程**仍然**必须用专用目录 ⇒ ⇒ 否则「碰巧对」也会让这条变绿。
	t.Setenv("GOCACHE", filepath.Join(t.TempDir(), "ambient-cache"))

	env := hollowBuildEnv(root)
	got := envValueLast(env, "GOCACHE")
	want := hollowGoCacheDir(root)

	if got == "" {
		t.Fatalf("★ 掏空门子进程的环境里没有 GOCACHE ⇒ ⇒ 子构建会写进用户的全局缓存")
	}
	if got != want {
		t.Fatalf("★ 掏空门子进程的 GOCACHE 不是专用目录：期望 %s，实际 %s", want, got)
	}
	if got == envValueLast(os.Environ(), "GOCACHE") {
		t.Fatalf("★ 专用目录与环境的 GOCACHE 相同（%s）⇒ ⇒ 隔离没有生效", got)
	}
	// ★★ **必须取「最后一个」**：★ 断言这个读取口径本身（★ 否则「取第一个」的实现
	// ★★ 会拿到用户那个全局缓存 ⇒ ⇒ 这条断言就变成恒真）。
	if first := envValueFirst(env, "GOCACHE"); first == got {
		t.Fatalf("★ 环境切片里 GOCACHE 只有一个匹配项 ⇒ ⇒ 「取最后一个」这条口径没被覆盖到")
	}
}

// TestHollowoutGoCacheDirIsNotTracked —— ★★ 专用缓存目录**绝不能**被提交进仓库。
func TestHollowoutGoCacheDirIsNotTracked(t *testing.T) {
	root := hollowRepoRoot(t)
	dir := hollowGoCacheDir(root)

	rel, err := filepath.Rel(root, dir)
	if err != nil {
		t.Fatalf("换算专用缓存目录的相对路径失败：%v", err)
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, ".build-local/") {
		t.Fatalf("★ 专用缓存目录不在 `.build-local/` 下（实际 %s）⇒ ⇒ 可能被提交进仓库", rel)
	}
	// ★★ `git check-ignore` 是**问 git 自己**（★ 而不是问我们自己的常量）⇒ ⇒
	// ★★ 独立于实现（★ 万一哪天 `.gitignore` 改了，这条会红）。
	out, err := exec.Command("git", "-C", root, "check-ignore", "-v", rel).CombinedOutput()
	if err != nil {
		t.Fatalf("★ 专用缓存目录 %s 不在 .gitignore 里（★ git check-ignore rc=%v）：%s\n"+
			"★ ⇒ ⇒ 它会被提交进仓库 ⇒ ⇒ 必须挪到已被忽略的路径下",
			rel, err, strings.TrimSpace(string(out)))
	}
}

// TestPruneHollowGoCacheRespectsSizeCap —— ★★ 剪枝必须**真的会删**，
// ★ 且**没超上限时不能删**（★ 否则每次跑都把缓存清了 ⇒ ⇒ 门会一直冷）。
func TestPruneHollowGoCacheRespectsSizeCap(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(blob, make([]byte, 4096), 0o644); err != nil {
		t.Fatalf("写测试文件失败：%v", err)
	}

	// ★ 没超上限 ⇒ 不删。
	pruned, err := pruneHollowGoCache(dir, 1<<20)
	if err != nil {
		t.Fatalf("剪枝（未超上限）报错：%v", err)
	}
	if pruned {
		t.Fatalf("★ 未超上限却被剪了（★ ⇒ 门会一直冷编译）")
	}
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("★ 未超上限时文件不该被删：%v", err)
	}

	// ★ 超上限 ⇒ 删掉整个目录。
	pruned, err = pruneHollowGoCache(dir, 1024)
	if err != nil {
		t.Fatalf("剪枝（超上限）报错：%v", err)
	}
	if !pruned {
		t.Fatalf("★ 超上限却没剪（★ ⇒ 专用缓存会无界增长 ⇒ ⇒ 又会撑爆磁盘）")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("★ 超上限后目录还在（err=%v）⇒ ⇒ 剪枝没真删", err)
	}

	// ★ 目录不存在 ⇒ **不是错误**（★ 幂等）。
	pruned, err = pruneHollowGoCache(filepath.Join(dir, "nope"), 1)
	if err != nil {
		t.Fatalf("★ 剪枝不存在的目录不该报错：%v", err)
	}
	if pruned {
		t.Fatalf("★ 目录不存在却被判成「剪过」")
	}
}

// envValueFirst 取环境变量切片里**第一个**匹配项的值（★ 空 ⇒ 没这个变量）。
//
// ★ 只服务于上面那条「读取口径」断言（★ 证明切片里**确实有多个** `GOCACHE`）。
func envValueFirst(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

// ── ④ 判据：★ 上限**常量本身**（★ r1047 轮补的已知缺口） ──

// TestHollowoutGoCacheSizeCapIsBounded —— ★★ 上限**常量本身**必须落在有界区间里。
//
// ★★★★ **为什么这道门必须存在（★ 已知缺口，★ r1046 轮自己写下的）**：
// ★★★★ 既有那三条判据里，「剪枝」那条钉的是【**函数行为**】（★ 给定 maxBytes
// ★★★★ 时剪不剪）⇒ ⇒ ★★★★ **常量本身**从来没有任何门管 ⇒ ⇒ ★★★★ 有人把
// ★★★★ `hollowGoCacheMaxBytes` 改成 `math.MaxInt64` ⇒ ⇒ ★★ 剪枝函数**行为
// ★★★★ 完全正确**（★ 该剪的照剪、该不剪的不剪）⇒ ⇒ ★★ **既有的门一条都不红**
// ★★★★ ⇒ ⇒ ★★ 而专用缓存重新变成**无界增长** ⇒ ⇒ ⇒ 磁盘又会被撑爆
// ★★★★ ⇒ ⇒ ★★ 而**红的原因仍然不指向真正的原因**（★ 是磁盘，不是门有洞）。
//
// ★★ **为什么是「带」而不是「等于 6 GiB」**：★ 钉死成 6 GiB ⇒ ⇒ 任何有意
// ★★ 的调参（★ 磁盘小的机器想调小、缓存增长变快了想调大）都会变红 ⇒ ⇒ 门变成
// ★★ 「不许调参」的官僚门 ⇒ ⇒ ★★ 而门真正要挡的是【无界】这个形态 ⇒ ⇒
// ★★ **带内自由、带外是缺陷**。
func TestHollowoutGoCacheSizeCapIsBounded(t *testing.T) {
	got := hollowGoCacheMaxBytes

	if got < hollowGoCacheCapFloor {
		t.Fatalf("★ 专用缓存上限 %d 字节（%.2f GiB）低于下界 %d 字节（%.2f GiB）⇒ ⇒ "+
			"掏空门每跑一次就必然超限 ⇒ ⇒ **每次都全清** ⇒ ⇒ 门永远冷编译"+
			"（★ 实测冷 213.35s vs 热 153.99s）",
			got, float64(got)/(1<<30),
			hollowGoCacheCapFloor, float64(hollowGoCacheCapFloor)/(1<<30))
	}
	if got > hollowGoCacheCapCeiling {
		t.Fatalf("★ 专用缓存上限 %d 字节（%.2f GiB）超过上界 %d 字节（%.2f GiB）⇒ ⇒ "+
			"专用缓存**无界增长** ⇒ ⇒ 又会撑爆磁盘"+
			"（★ 上界的作用是挡住 math.MaxInt64 / math.MaxInt32 这类形态）",
			got, float64(got)/(1<<30),
			hollowGoCacheCapCeiling, float64(hollowGoCacheCapCeiling)/(1<<30))
	}
}

// ── ⑤ 判据：★ 上限必须【真的接到剪枝上】 ──

// hollowCapArgKind 分类【剪枝调用的上限实参】的形态。
//
// ★ "pinned"    = 恰好是被钉住的常量标识符（★ **唯一允许的「生产上限」形态**）
// ★ "synthetic" = 纯整数字面量的算术表达式（★ 如 `1<<20` / `1024` / `1`）
// ★              ⇒ ⇒ 单测里那种「故意用小上限造出超限」的**假上限**
// ★ "bypass"    = 其余一切（★ 如 `math.MaxInt64` / 某个变量 / 某个函数调用）
// ★              ⇒ ⇒ **绕过了**被钉住的上限
func hollowCapArgKind(e ast.Expr) string {
	if ident, ok := e.(*ast.Ident); ok && ident.Name == "hollowGoCacheMaxBytes" {
		return "pinned"
	}
	if hollowIntLiteralExpr(e) {
		return "synthetic"
	}
	return "bypass"
}

// hollowIntLiteralExpr 判「这个表达式是不是纯整数字面量的算术组合」。
func hollowIntLiteralExpr(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.INT
	case *ast.ParenExpr:
		return hollowIntLiteralExpr(v.X)
	case *ast.UnaryExpr:
		if v.Op != token.SUB && v.Op != token.ADD {
			return false
		}
		return hollowIntLiteralExpr(v.X)
	case *ast.BinaryExpr:
		return hollowIntLiteralExpr(v.X) && hollowIntLiteralExpr(v.Y)
	}
	return false
}

// TestHollowoutGoCacheSizeCapReachesPrune —— ★★ 上限必须【真的接到剪枝上】。
//
// ★★★★ **为什么「常量有界」还不够**：★ 常量有界 ⇒ ⇒ 上限**本身**是可信的
// ★★★★ ⇒ ★★ 但若**剪枝那一步没有用它**（★ 例如调用点改传 `math.MaxInt64`，
// ★★★★ 或者干脆把剪枝调用删掉）⇒ ⇒ ★★ 上限就是一个【没人读的装饰】
// ★★★★ ⇒ ⇒ ★★ 专用缓存照样无界增长 ⇒ ⇒ ⇒ 而**既有的门一条都不红**。
//
// ★★★★ **为什么判据用 AST 而不是正则**：★ 正则会把【注释里写的调用】也算进去
// ★★★★ ⇒ ⇒ ★★ 一条注释就能把这条门变成假绿 ⇒ ⇒ ★★ AST 只认【真的调用表达式】
// ★★★★ ⇒ ⇒ 注释天然不进判据（★ `go/parser` 把注释挂在 `File.Comments` 上，
// ★★★★ 不在表达式树里）⇒ ⇒ ★★ **判据与「注释里的假调用」天然免疫**。
//
// ★★ **判据的两条**：
// ① 至少有一处调用把**被钉住的常量**接到剪枝上（★ ⇒ 上限不是装饰）；
// ② 没有任何一处调用**绕过**它（★ ⇒ 上限是唯一的真值）。
func TestHollowoutGoCacheSizeCapReachesPrune(t *testing.T) {
	root := hollowRepoRoot(t)
	dir := filepath.Join(root, "scripts", "ursmcheck")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", dir, err)
	}

	fset := token.NewFileSet()
	var pinned, synthetic, bypass []string

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", path, err)
		}
		// ★ 预筛：只有【提到剪枝函数】的文件才值得解析（★ 162 个文件逐个解析太慢）。
		if !strings.Contains(string(src), "pruneHollowGoCache") {
			continue
		}
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("解析 %s 失败：%v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != "pruneHollowGoCache" {
				return true
			}
			where := fmt.Sprintf("%s:%d", e.Name(), fset.Position(call.Pos()).Line)
			if len(call.Args) != 2 {
				bypass = append(bypass, where+"（★ 实参数不是 2 个）")
				return true
			}
			switch hollowCapArgKind(call.Args[1]) {
			case "pinned":
				pinned = append(pinned, where)
			case "synthetic":
				synthetic = append(synthetic, where)
			default:
				bypass = append(bypass, where+"（★ 上限实参绕过了被钉住的常量）")
			}
			return true
		})
	}

	if len(pinned) == 0 {
		t.Fatalf("★ 本包里没有任何一处剪枝调用把**被钉住的上限常量**接上去"+
			"（★ 实测：单测里的假上限 %d 处、绕过 %d 处）⇒ ⇒ "+
			"上限是个没人读的装饰 ⇒ ⇒ 专用缓存照样无界增长",
			len(synthetic), len(bypass))
	}
	if len(bypass) > 0 {
		t.Fatalf("★ %d 处剪枝调用**绕过**了被钉住的上限（★ ⇒ 上限不是唯一真值）：\n  %s",
			len(bypass), strings.Join(bypass, "\n  "))
	}
}
