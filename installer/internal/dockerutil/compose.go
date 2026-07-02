// Package dockerutil 提供 docker compose 封装
package dockerutil

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Compose docker compose 封装
type Compose struct {
	ComposeFile string  // compose.yml 路径
	ProjectName string  // 项目名（默认目录名）
	WorkDir     string  // 工作目录
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
	cmd := c.command("up", "-d")
	cmd.Stdout = nil
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
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
	cmd := c.command(args...)
	out, err := cmd.CombinedOutput()
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
	cmd := c.command(args...)
	return cmd.Run()
}

// Logs 查看日志
func (c *Compose) Logs(follow bool, tail string) error {
	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}
	if tail != "" {
		args = append(args, "--tail", tail)
	}
	cmd := c.command(args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// PS 列出容器状态
func (c *Compose) PS() (string, error) {
	cmd := c.command("ps")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// IsRunning 检查指定容器是否在运行
func (c *Compose) IsRunning(containerName string) bool {
	cmd := c.command("ps", "--status", "running", "--format", "{{.Names}}")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), containerName)
}

// command 构造 docker compose 命令
func (c *Compose) command(args ...string) *exec.Cmd {
	fullArgs := append([]string{"compose", "-f", c.ComposeFile, "-p", c.ProjectName}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return exec.CommandContext(ctx, "docker", fullArgs...)
}

// indentOutput 在每行前加缩进
func indentOutput(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

// MustBuffer 辅助函数
func MustBuffer(b bytes.Buffer) string {
	return b.String()
}
