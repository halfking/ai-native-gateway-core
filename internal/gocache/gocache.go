// Package gocache —— 仓内【专用 Go 构建缓存】的统一入口。
//
// # 为什么需要这个包（★ 2026-10-28，★ r1046/r1047 轮）
//
// ★★★ **背景**：★ 仓里有若干「门 / 测试 / 工具」会在**子进程**里跑 `go build` /
// ★★★ `go test` / `go vet` / `go run` ⇒ ⇒ 子进程会往 **Go 的构建缓存**里写产物。
//
// ★★★ **那个构建缓存默认是【用户那个全局的】**（★ macOS 上是
// ★★★ `~/Library/Caches/go-build`）⇒ ⇒ ★★★ 而 Go 的构建缓存是**内容寻址**的
// ★★★ （★ action ID = 全部输入内容 + 构建配置的哈希）⇒ ⇒ ★★★ **它不可能返回
// ★★★ 陈旧的产物** ⇒ ⇒ ★★★ 但它**会无限增长**：★ 树每变一次（★ 改一行代码、
// ★★★ 合并一次、换一个分支……），编译产物的 action ID 就全变 ⇒ ⇒ ★★★ 旧的那些
// ★★★ **永远不会再被命中** ⇒ ⇒ ★★★ ⇒ 事实上的垃圾 ⇒ ⇒ ★★★ 而它们躺在用户的
// ★★★ 全局缓存里（★ Go 自己只按 5 天 trim）⇒ ⇒ ★★★ ⇒ **磁盘被撑爆**。
//
// ★★★★ **实测（★ r1046 轮，本机）**：★ 掏空门每跑一次（★ 在**新树**上）往
// ★★★★ 全局 `GOCACHE` 里塞 **~1.3 GB** ⇒ ⇒ 本机全局缓存一度到 **72 GB**
// ★★★★ ⇒ ⇒ 磁盘 100% 满 ⇒ ⇒ 全量 `go test ./...` **编译阶段**就报
// ★★★★ `no space left on device` ⇒ ⇒ ★★ **红的原因不指向真正的原因**
// ★★★★ （★ 真正的原因是磁盘，不是门有洞）。
//
// # 本包做什么
//
// ★ ① **给一个「专用目录」**：★ `<仓根>/.build-local/.gocache-<purpose>`
// ★ ② **给子进程指过去**：★ 在 `os.Environ()` **之后**追加 `GOCACHE=<专用目录>`
// ★ ⇒ ⇒ ★ 靠 Go `exec` 的「**后出现的同名变量赢**」语义 ⇒ ⇒ ★ 即便用户环境里
// ★ 已经设了 `GOCACHE`，子进程也会用专用目录。
//
// ★★ **为什么专用目录放在仓内 `.build-local/`**：★ ① 它**已经在 `.gitignore` 里**
// ★★ （★ `.gitignore:42` ⇒ `.build-local/`）⇒ ⇒ **绝不会被提交进仓库**；
// ★★ ② 它跟着仓走 ⇒ ⇒ `git clean -xfd` 能顺手回收它；★ ③ 它是**仓库内**的路径
// ★★ ⇒ ⇒ 与 `scripts/deploy-seamless.sh` 等既有 `.build-local/.gocache` 的约定**同款**。
//
// ★★ **为什么「隔离」不牺牲任何正确性**：★ Go 的构建缓存是**内容寻址**的
// ★★ ⇒ ⇒ ★ **它不可能返回陈旧的产物** ⇒ ⇒ ★ 隔离只改「产物落在哪」，不改
// ★★ 「产物是不是对的」⇒ ⇒ ★ **零正确性代价**。
//
// # 既有实现的分工（★ 不要重复造）
//
// ★ 掏空门（★ `scripts/ursmcheck/hollowout_gocache_test.go`）有**自己**的一套
// ★ 专用缓存实现（★ 目录 `.build-local/.gocache-hollowout`）⇒ ★ 那套**额外**带
// ★ 「尺寸上限 + 超限整目录删除」（★ 因为掏空门每跑一次就涨 ~1.3 GB ⇒ ⇒ 它是
// ★ 本仓唯一「会自己撑爆自己」的专用缓存）⇒ ★ ⇒ **本包不重复那个逻辑**。
package gocache

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RelDirPrefix 是【专用缓存目录】的**相对仓根**前缀。
//
// ★ 必须在 `.build-local/` 下（★ 那一层已在 `.gitignore:42`）⇒ ⇒ 专用缓存
// ★ **绝不会被提交进仓库**。
const RelDirPrefix = ".build-local/.gocache-"

// Dir 返回 purpose 对应的【专用构建缓存目录】的绝对路径（★ **不创建**）。
//
// ★ purpose 应当能自解释（★ 如 "hollowout" / "integration-vet" / "plugin-runtime"）。
func Dir(root, purpose string) string {
	return filepath.Join(root, filepath.FromSlash(RelDirPrefix+purpose))
}

// Value 返回给子进程用的 `GOCACHE=...` 环境变量条目（★ 单条字符串）。
//
// ★ ★★ **为什么单独提供这个**：★ 有些调用点要**自己拼**子进程的环境
// ★ ★★ （★ 例如还要额外加 `GO111MODULE=off`、`PATH=…`）⇒ ⇒ 此时用
// ★ ★★ `os.Environ()` + 本函数返回值即可 ⇒ ⇒ **不必**退回到裸 `os.Environ()`。
func Value(root, purpose string) string {
	return "GOCACHE=" + Dir(root, purpose)
}

// Env 返回给子进程用的完整环境（★ `os.Environ()` + 【GOCACHE 覆盖】）。
//
// ★ ★★ **注意**：★ `os.Environ()` 里原有的 `GOCACHE` **仍然留在切片里**
// ★ ★★ ⇒ ⇒ ★ 所以读这个切片时**必须取最后一个匹配**（★ Go `exec` 的语义是
// ★ ★★ 「后出现的同名变量赢」）⇒ ⇒ ★ 本包**总是追加在最后** ⇒ ⇒ 天然满足。
func Env(root, purpose string) []string {
	env := append([]string{}, os.Environ()...)
	return append(env, Value(root, purpose))
}

// Ensure 创建专用缓存目录（★ 幂等）。
//
// ★ ★★ **为什么需要显式创建**：★ Go 的构建缓存**自己会建**目录 ⇒ ⇒ 所以
// ★ ★★ 【子进程】其实不需要预先创建 ⇒ ⇒ ★ 本函数只给【需要自己往里写东西】
// ★ ★★ 的调用点用（★ 以及给「想确认路径可用」的调用点用）。
func Ensure(root, purpose string) error {
	return os.MkdirAll(Dir(root, purpose), 0o755)
}

// RepoRoot 返回仓根（★ 问 **git 自己** ⇒ ⇒ 独立于任何硬编码的相对路径）。
//
// ★ ★★ **为什么不用「往上找 go.mod」**：★ 那样每个调用点都要自己写一遍
// ★ ★★ 「往上找」⇒ ⇒ ★ 抄一遍就有两份 ⇒ ⇒ ★ 而且【仓内 module 边界】不唯一
// ★ ★★ （★ `installer/` 是独立 module）⇒ ⇒ ★ 问 git 最不容易错。
func RepoRoot() (string, error) {
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
