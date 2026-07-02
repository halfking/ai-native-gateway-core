// Package dockerutil 提供 docker compose 封装
package dockerutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// 默认超时：docker compose 操作通常不应超过 10 分钟
const defaultTimeout = 10 * time.Minute

// Compose docker compose 封装
type Compose struct {
	ComposeFile string // compose.yml 路径
	ProjectName string // 项目名（默认目录名）
	WorkDir     string // 工作目录
}

// NewCompose 创建 Compose
func NewCompose(composeFile, projectName, workDir string) *Compose {
	return &Compose{
		ComposeFile: composeFile,
		ProjectName: projectName,
		WorkDir:     workDir,
	}
}

// Up 启动容器（docker compose up -d）
func (c *Compose) Up(logger func(string)) error {
	logger("▶ 启动 Docker 容器 ...")
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	out, err := c.runCompose(ctx, "up", "-d")
	if err != nil {
		return fmt.Errorf("docker compose up 失败: %w\n%s", err, string(out))
	}
	logger("  " + indentOutput(string(out)))
	return nil
}

// Down 停止容器
func (c *Compose) Down(logger func(string), removeVolumes bool) error {
	logger("▶ 停止 Docker 容器 ...")
	args := []string{"down"}
	if removeVolumes {
		args = append(args, "-v")
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	out, err := c.runCompose(ctx, args...)
	if err != nil {
		return fmt.Errorf("docker compose down 失败: %w\n%s", err, string(out))
	}
	logger("  " + indentOutput(string(out)))
	return nil
}

// Restart 重启服务
func (c *Compose) Restart(service string) error {
	args := []string{"restart"}
	if service != "" {
		args = append(args, service)
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	_, err := c.runCompose(ctx, args...)
	return err
}

// Logs 查看日志（follow 时直接输出到 os.Stdout/Stderr）
func (c *Compose) Logs(follow bool, tail string) error {
	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}
	if tail != "" {
		args = append(args, "--tail", tail)
	}
	// follow 模式没有明确超时，用较长上下文
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()
	cmd := c.buildCmd(ctx, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// PS 列出容器状态
func (c *Compose) PS() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	cmd := c.buildCmd(ctx, "ps")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// IsRunning 检查指定容器是否在运行
func (c *Compose) IsRunning(containerName string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	cmd := c.buildCmd(ctx, "ps", "--status", "running", "--format", "{{.Names}}")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), containerName)
}

// buildCmd 构造 docker compose 命令（不带 timeout，由调用方在 ctx 里设置）
func (c *Compose) buildCmd(ctx context.Context, args ...string) *exec.Cmd {
	fullArgs := append([]string{"compose", "-f", c.ComposeFile, "-p", c.ProjectName}, args...)
	if c.WorkDir != "" {
		cmd := exec.CommandContext(ctx, "docker", fullArgs...)
		cmd.Dir = c.WorkDir
		return cmd
	}
	return exec.CommandContext(ctx, "docker", fullArgs...)
}

// runCompose 执行 compose 命令并返回合并输出（CombinedOutput）
// 注意：不要在调用前设置 cmd.Stdout/Stderr，否则会覆盖 CombinedOutput 的内部 buffer
func (c *Compose) runCompose(ctx context.Context, args ...string) ([]byte, error) {
	cmd := c.buildCmd(ctx, args...)
	return cmd.CombinedOutput()
}

// indentOutput 在每行前加缩进
func indentOutput(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}
