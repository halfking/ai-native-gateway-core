package ursm_test

// hollowout_lock_test.go —— 【整仓掏空锁】的**实现（公共部分）**与**自证**
// （★ 2026-10-28 R87 轮收尾）。
//
// # 为什么需要这一组文件
//
// 掏空法门要把被跟踪的登记册**真的掏空**（写盘）⇒ ⇒ **它变异共享工作树**。
// ⇒ ⇒ **同一个 checkout 里并发跑两个 `go test` 就会同时变异同一批文件**。
//
// ★ 竞态的具体形状（★ 三种结局，★ 没有一种是「良性」）：
//
//	跑 A：备份 F（原文）→ 掏空 F
//	跑 B：自愈预检看到 F 有备份 ⇒ ⇒ **把 F 还原成原文** ⇒ ⇒ **A 的掏空被抹掉了**
//	     ⇒ ⇒ A 拿到的读数是「读方在【原文】上绿」⇒ ⇒ **A 误判成 still-green（门有洞）**
//	或反向：★ A 先备份 ⇒ B 在 A 掏空**之后**才备份 ⇒ ⇒ **B 的备份 = 掏空版**
//	     ⇒ ⇒ B 收尾「还原」⇒ ⇒ **F 被永久留在掏空态**（★ 台账在仓里被掏空）
//	或交错：★ 两者交错 ⇒ ⇒ 还原后 md5 对不上 ⇒ ⇒ 门红（★ 这一种反而是好的）。
//
// ★★ 【诚实边界】：★ 本仓**有多个 checkout**（★ `llm-gateway-go` / `-3` / `-5` / `-cursor`）
// ⇒ ⇒ **跨 checkout 不共享工作树** ⇒ ⇒ 跨 checkout 的两个跑**撞不上**。
// ★★ 所以这把锁防的是【**同一个 checkout 内**并发跑两个 `go test`】——
// ★★ 而那是**常见操作**：★ 后台跑整包 ＋ 前台跑单条（★ 本轮自己就做过一次）。
// ★★ 它的代价是**一个 flock ＋ 一次轮询**；收益是**台账不会被留在掏空态**。
//
// ★⇒ ⇒ 所以必须有**跨进程互斥**：★ 同一时刻只允许一个掏空跑在变异工作树。
//
// # 修法
//
// ★ 用**文件锁**（★ unix: `flock`；★ windows: `LockFileEx`）锁住【整仓一把锁】
// ⇒ ⇒ 第二个跑**等待**（而不是并发变异）⇒ ⇒ 等到前一个跑完再开始。
// ★ 锁**只覆盖【变异窗口】**（★ 读方跑本身不写工作树）⇒ ⇒ 不拖慢无关的门。
//
// ★ 超时怎么办：**红**（★ 不是跳过）⇒ ⇒ 因为「跳过」在退出码上与「跑了 0 条」不可区分
// ⇒ ⇒ 与本线那条「★『跑过了』与『跑了 0 条』在退出码上不可区分」**同族**。
//
// ★ 为什么不用「目录级锁」：★ 登记册散落在 `scripts/` / `web/` / `bg/` 等多个目录
// ⇒ ⇒ 逐目录锁**锁不住跨目录的同一个文件**（★ 而 `.tsv` 与结构化登记册分处两地）
// ⇒ ⇒ 必须一把**整仓锁**。
//
// ★ 为什么这些文件都带 `_test.go` 后缀：★ `scripts/ursmcheck` 目录下
// ★ **没有任何非测试文件**（★ 154 个文件全是 `_test.go`）⇒ ⇒ 若把实现放进普通 `.go`，
// ⇒ ⇒ 它会成为该目录的**基包**（★ 且必须声明 `package ursm` 而不是 `package ursm_test`）
// ⇒ ⇒ 与仓内既有约定（★ 全部测试文件）不一致 ⇒ ⇒ 这里一律用 `_test.go`。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// hollowoutLockName 是锁文件名。
//
// ★ 放在**仓根**⇒ ⇒ 任何进程（不论 cwd 在哪）都能算出同一把锁 ⇒ ⇒ 才互斥得住。
// ★ 它是**未跟踪**文件 ⇒ ⇒ 不在 `git ls-files` 的输入集合里
// ⇒ ⇒ 不会污染「判据的输入集合必须确定」这条纪律（★ 覆盖判据只认 git 追踪的文件）。
const hollowoutLockName = ".ursmcheck-hollowout.lock"

// hollowoutLockWait 是拿锁的等待上限。
//
// ★ 为什么是 15 分钟：★ `.tsv` 侧整门实测 ~5.5 分钟（★ 结构化侧 ~2.3 分钟）
// ⇒ ⇒ 并发时第二个跑最多等一轮（★ 加上自身那一轮）⇒ ⇒ 15 分钟足够。
//
// ★★ 又为什么**不能更长**：★ Go 的 `testing` 超时是 **alarm goroutine 里的 panic**
// ⇒ ⇒ 进程被杀时**其它 goroutine 的 defer 不跑**⇒ ⇒ 掏空态会留在盘上
// ⇒ ⇒ **等待上限必须显著小于测试超时预算**（★ 本轮统一 `-timeout=1800s`）。
const hollowoutLockWait = 15 * time.Minute

// hollowoutLockPath 返回整仓掏空锁的路径。
func hollowoutLockPath(root string) string {
	return filepath.Join(root, hollowoutLockName)
}

// lockHollowoutShared 取得【整仓掏空锁】，返回释放函数与【尝试次数】。
//
// ★ 返回值：
//   - (release, attempts, nil)：★ 拿到锁 ⇒ ⇒ 调用方**必须**在变异完成后调用 release()
//   - (nil, attempts, err)：★ 等待超时 / 锁不可用 ⇒ ⇒ 调用方**必须让门红**（★ 不能跳过）
//
// ★★ 【为什么带「尝试次数」出来】：★ 它是「这个持有者**确实等过**」的**不依赖时序**的证据
// ★★ （★ 见 unix 版的注释：★ 时序断言会被仓内的余量门要求登记实测台账）。
func lockHollowoutShared(root string, wait time.Duration) (func(), int, error) {
	path := hollowoutLockPath(root)
	release, attempts, err := lockHollowoutFile(path, wait)
	if err != nil {
		return nil, attempts, fmt.Errorf("拿整仓掏空锁失败（%s）：%w", path, err)
	}
	return release, attempts, nil
}

// ── 自证 ────────────────────────────────────────────────────────────────────

// TestHollowoutSharedLockExcludesSecondHolder 证明【整仓掏空锁】真的互斥。
//
// ★ 三段断言：
// ① 第一个持有者拿得到；
// ② ★ 第二个持有者**拿不到**，且它**至少轮询过一次**（★ ⇒ ⇒ 它确实在等 ⇒ ⇒ 而不只是「路径写错了」）；
// ③ 释放之后能再拿到（★ ⇒ ⇒ 锁不是「一次性」的 ⇒ ⇒ 连续 35 条目不会把自己锁死）。
//
// ★★ 【为什么断言「轮询次数」而不是「等了多少毫秒」】：★ 仓内 `internal/clockguard`
// ★★ 会把 `elapsed < <时长字面量>` 判成【判别型下界】⇒ ⇒ 新增一处就得上
// ★★ `measured_margins.tsv` 实测台账（★ 要在本机量 10~30 次自然值）⇒ ⇒ 代价与收益不成比例。
// ★★ ⇒ ⇒ **正确做法是不要引入时序依赖**：★ 「确实等过」由【轮询次数】表达 ⇒ ⇒ 与机器快慢无关。
//
// ★ 为什么能在**单进程内**自证：★ `flock` 的锁挂在【open file description】上
// （★ POSIX 规定）⇒ ⇒ 同一进程用两个 fd 锁同一文件**也互斥**
// ⇒ ⇒ 不需要起子进程就能证明「第二个持有者被挡住」。
func TestHollowoutSharedLockExcludesSecondHolder(t *testing.T) {
	dir := t.TempDir()

	release, firstAttempts, err := lockHollowoutShared(dir, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("★ 第一个持有者拿不到锁 ⇒ ⇒ 锁根本不可用：%v", err)
	}
	if firstAttempts != 1 {
		t.Fatalf("★ 第一个持有者应该**一次就拿到**（★ 它没有竞争者）⇒ 实际尝试 %d 次", firstAttempts)
	}

	func() {
		// ★ 先注册释放 ⇒ ⇒ 下面任何一次 t.Fatalf（Goexit）都会把锁放掉。
		defer release()

		second, attempts, err := lockHollowoutShared(dir, 200*time.Millisecond)
		if err == nil {
			second()
			t.Fatalf("★ 第二个持有者**拿到了**锁 ⇒ ⇒ 互斥失效 ⇒ ⇒ 并发跑会互相备份对方的掏空版 " +
				"⇒ ⇒ 台账可能被永久留在掏空态")
		}
		// ★ 它必须**至少轮询过一次**⇒ ⇒ 否则「拿不到」可能只是「路径写错了 / 一次就放弃了」。
		if attempts < 2 {
			t.Fatalf("★ 第二个持有者只尝试了 %d 次就放弃了 ⇒ ⇒ 它**没等** ⇒ ⇒ 「拿不到」这个判定不实",
				attempts)
		}
	}()

	// ★ 释放之后必须能再拿到（★ 连续跑 35 个条目 ⇒ ⇒ 不能把自己锁死）。
	release2, _, err := lockHollowoutShared(dir, 2*time.Second)
	if err != nil {
		t.Fatalf("★ 释放之后拿不到锁 ⇒ ⇒ 锁是一次性的 ⇒ ⇒ 连续多条目会把自己锁死：%v", err)
	}
	release2()
}

// TestHollowoutLockFileIsUntrackedAndIgnored 证明【锁文件】不会污染判据的输入集合。
//
// ★ 纪律：「判据的【输入集合】必须确定」⇒ ⇒ 覆盖判据只认 **git 追踪**的文件
// ⇒ ⇒ 锁文件若被追踪，它就会进入普查表 / 覆盖判据的视野 ⇒ ⇒ 那是噪音。
//
// ★ 三段断言：
// ① 锁落在**仓根**（★ 任何 cwd 都算得出同一把锁）；
// ② 它被 `.gitignore` 登记（★ ⇒ ⇒ 未跟踪 ⇒ ⇒ 不在输入集合里）；
// ③ `git check-ignore` 复核（★ 不靠 `.gitignore` 文本的肉眼核对）。
func TestHollowoutLockFileIsUntrackedAndIgnored(t *testing.T) {
	root := hollowRepoRoot(t)

	lockPath := hollowoutLockPath(root)
	if filepath.Dir(lockPath) != filepath.Clean(root) {
		t.Fatalf("★ 锁文件必须落在仓根（★ 任何 cwd 都算得出同一把锁）⇒ 期望 %s，实际 %s", root, lockPath)
	}
	if filepath.Base(lockPath) != hollowoutLockName {
		t.Fatalf("★ 锁文件名不对：期望 %s，实际 %s", hollowoutLockName, filepath.Base(lockPath))
	}

	// ★ `git check-ignore` 退出码 0 ⇒ ⇒ 该路径被忽略。
	cmd := exec.Command("git", "-C", root, "check-ignore", "-q", hollowoutLockName)
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("跑 git check-ignore 失败：%v", err)
		}
		t.Fatalf("★ 锁文件 %s **没有**被 .gitignore 登记 ⇒ ⇒ 它会进入「git 追踪文件」的视野 "+
			"⇒ ⇒ 污染普查表 / 覆盖判据的输入集合", hollowoutLockName)
	}

	// ★ 反向断言：★ 锁文件**跑完会留在盘上**（★ `flock` 释放的是「锁」不是「文件」）
	// ⇒ ⇒ 断言不能是「文件不存在」（★ 那会在**第二次跑**时恒红 ⇒ ⇒ 是我自己的自伤）。
	// ⇒ ⇒ 正确的断言是「它**没有被 git 追踪**」⇒ ⇒ 即便它在盘上，也不在判据的输入集合里。
	tracked := exec.Command("git", "-C", root, "ls-files", "--error-unmatch", hollowoutLockName)
	tracked.Stdout = nil
	tracked.Stderr = nil
	if err := tracked.Run(); err == nil {
		t.Fatalf("★ 锁文件 %s **被 git 追踪**了 ⇒ ⇒ 它会进入「git 追踪文件」的视野 "+
			"⇒ ⇒ 污染普查表 / 覆盖判据的输入集合", hollowoutLockName)
	}
	// ★ 若它此刻在盘上 ⇒ ⇒ 必须是**零字节**（★ 锁文件不存内容 ⇒ ⇒ 不可能夹带信息）。
	if st, err := os.Stat(lockPath); err == nil && st.Size() != 0 {
		t.Fatalf("★ 锁文件 %s 有 %d 字节 ⇒ ⇒ 锁文件应当零字节（★ 它不承载任何数据）",
			hollowoutLockName, st.Size())
	}
}
