package dockerutil

// docker 命令的**超时收口**（2026-11-23）
//
// ★★ **为什么需要它（★ 实测）**：★★
//
//　★ Docker 守护进程僵死时（★ `docker ps` / `docker exec` 的**客户端永不返回**），
//　★ 而安装链上原有的调用点**全部是 `exec.Command`（★ 没有任何上限 ★）**：
//
//	★ ① `dbinit.Runner.WaitForPG`（★ 等 PG 就绪、★ 装机的第一步 ★）
//	★ ② `dbinit.Runner.VerifySchema`（★ 装完的校验 ★）
//	★ ③ `HealthChecker.containerRunning`（★ `docker ps` ★）
//	★ ④ `HealthChecker.checkPG`（★ `pg_isready` ★）
//	★ ⑤ `HealthChecker.checkRedis`（★ `redis-cli ping` ★）
//	★ ⑥ `HealthChecker.checkSchema`（★ `psql` 数表 ★）
//
//　★ ⇒ ★★ **安装链在 Docker 无响应时会【无限挂住】** ⇒ ★★ 连
//　★ 「PostgreSQL 启动超时 (Ns)」这句话**都打不出来** ★★ ⇒ ★★
//　★ **运维看到的是「卡住」而不是「失败」，★ 根因无从判断** ★★。
//
// ★★ **与本轮修掉的 census 门同族**：★★
// ★ **量具与工具在环境异常时必须「到点收口」、★ 而不是「无限挂」** ★★
// ★ （★ census 门那条见 `scripts/ursmcheck/index_reader_census_sql_test.go` ★）。
//
// ★★ **为什么这里不需要 census 门那种「进程组整组杀」**：★★
// ★ `docker` CLI 是**直接子进程**（★ 不是 shell、★ 不套 `sh -c` ★）
// ★ ⇒ ★ `exec.CommandContext` 到期 kill 它**就是全部** ⇒ ★★
// ★ ★ 不存在「孙进程持有 stdout 管道导致 `Wait()` 收不到 EOF」的问题 ★★
// ★ ⇒ ★★ 这里用 `cmd.Output()`（★ stdout 走管道、★ stderr 走 ExitError.Stderr ★）
// ★★ 是安全的：★ kill 掉 CLI ⇒ 管道写端全关 ⇒ ★ `Wait()` 正常返回 ★★。

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// 单次 docker 命令的默认上限（★ 按命令快慢分两档 ★）。
const (
	// DockerCmdTimeoutFast 是「快命令」的上限。
	//
	// ★ 覆盖 `docker ps` / `pg_isready` / `redis-cli ping` 这类
	// ★ **容器内一问一答**的命令（★ 正常亚秒级返回 ★）。
	DockerCmdTimeoutFast = 10 * time.Second

	// DockerCmdTimeoutSlow 是「可能慢」的上限。
	//
	// ★ 覆盖 `psql` 计数这类**要在库里真跑一句**的命令
	// ★ （★ 大库上可能明显慢于快命令 ★）。
	//
	// ★ 取 15s 而不是 30s：★ 健康检查是**串行 5 步**（★ 容器 ×1 → healthz → PG
	// ★ → Redis → Schema ★）⇒ ★ 慢档取 30s 会让**整条健康检查在最坏情况下
	// ★ 逼近 90s** ⇒ ★★ **慢门的墙钟预算要算总账、★ 不是单项** ★★。
	DockerCmdTimeoutSlow = 15 * time.Second
)

// RunDocker 跑一条 docker 命令，并保证「**到点一定返回**」。
//
// ★ 返回 `(stdout, error)`：
// ★   ① 成功 ⇒ stdout 是命令的标准输出、error 为 nil；
// ★   ② **超时** ⇒ error 里带「超时」二字 ＋ 底层 `context.DeadlineExceeded`；
// ★   ③ 命令本身失败（★ 非零退出、★ docker 不存在 ★）⇒ error 里带「失败」二字。
//
// ★★ **调用方要判「超时」还是「真失败」时，★ 请按 error 文本里的关键词区分** ★★
// ★ （★ 或者直接用 `errors.Is(err, context.DeadlineExceeded)` ★）。
func RunDocker(timeout time.Duration, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("docker 命令为空")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return out, fmt.Errorf("docker %s 超时（%s）: %w", args[0], timeout, ctx.Err())
		}
		return out, fmt.Errorf("docker %s 失败: %w", args[0], err)
	}
	return out, nil
}
