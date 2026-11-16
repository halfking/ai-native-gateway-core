package autoupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SourceBuildRequest 描述一次「从本地源码编译」的请求。
//
// 设计要点：
//   - **不改动用户的工作树**。若指定了 Ref，用 git worktree 在临时目录里
//     检出该 ref，编译完再摘掉；直接 checkout 到用户工作树会毁掉在途改动。
//   - 编译产物是**新二进制**，不会覆盖任何现有文件；替换由 Installer 负责。
type SourceBuildRequest struct {
	// RepoDir 源码仓库根目录（空=用 SourceBuilder 的默认目录）。
	RepoDir string `json:"repo_dir,omitempty"`
	// Ref git ref（tag / branch / commit）。空 = 用当前工作树的 HEAD 状态。
	Ref string `json:"ref,omitempty"`
	// Package 编译目标包路径（默认 ./cmd/gateway）。
	Package string `json:"package,omitempty"`
	// Output 输出二进制路径（空=落到临时目录）。
	Output string `json:"output,omitempty"`
	// Version 写入版本元数据的版本号（空=从源码树的 version.json 读取）。
	Version string `json:"version,omitempty"`
	// BuildSeq 写入版本元数据的构建序号（0=沿用源码树的值）。
	BuildSeq int `json:"build_seq,omitempty"`
}

// SourceBuildResult 是一次源码编译的结果。
type SourceBuildResult struct {
	BinaryPath   string `json:"binary_path"`
	Version      string `json:"version"`
	BuildSeq     int    `json:"build_seq"`
	GitSHA       string `json:"git_sha,omitempty"`
	VersionJSON  []byte `json:"-"`
	DurationMs   int64  `json:"duration_ms"`
	UsedWorktree bool   `json:"used_worktree"`
}

// SourceBuilder 从本地源码树编译新的网关二进制。
type SourceBuilder struct {
	repoDir string
	goBin   string
	gitBin  string
	timeout time.Duration
}

// SourceBuilderOption 用于覆盖默认行为。
type SourceBuilderOption func(*SourceBuilder)

// WithSourceRepoDir 指定源码仓库根目录。
func WithSourceRepoDir(dir string) SourceBuilderOption {
	return func(b *SourceBuilder) { b.repoDir = dir }
}

// WithGoBin 指定 go 可执行文件。
func WithGoBin(bin string) SourceBuilderOption {
	return func(b *SourceBuilder) { b.goBin = bin }
}

// WithGitBin 指定 git 可执行文件。
func WithGitBin(bin string) SourceBuilderOption {
	return func(b *SourceBuilder) { b.gitBin = bin }
}

// WithBuildTimeout 设定单次编译超时。
func WithBuildTimeout(d time.Duration) SourceBuilderOption {
	return func(b *SourceBuilder) { b.timeout = d }
}

// NewSourceBuilder 创建源码构建器。
func NewSourceBuilder(opts ...SourceBuilderOption) *SourceBuilder {
	b := &SourceBuilder{
		repoDir: "",
		goBin:   "go",
		timeout: 20 * time.Minute,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(b)
		}
	}
	return b
}

// Build 执行一次源码编译。
func (b *SourceBuilder) Build(ctx context.Context, req SourceBuildRequest) (*SourceBuildResult, error) {
	start := time.Now()

	repoDir := strings.TrimSpace(req.RepoDir)
	if repoDir == "" {
		repoDir = b.repoDir
	}
	if repoDir == "" {
		return nil, fmt.Errorf("source repo dir is not configured")
	}
	if info, err := os.Stat(repoDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("source repo dir %q is not a directory", repoDir)
	}

	buildCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	// Ref 指定 ⇒ 用临时 worktree 编译，绝不动用户工作树。
	worktreeDir := ""
	buildDir := repoDir
	if ref := strings.TrimSpace(req.Ref); ref != "" {
		// ★ 注意不要用 `:=`：那会把 worktreeDir 遮蔽成块内变量，
		//   外层的 UsedWorktree 判定就永远是 false（实测踩过）。
		tmpDir, err := os.MkdirTemp("", "llmgw-src-build-")
		if err != nil {
			return nil, fmt.Errorf("create worktree dir: %w", err)
		}
		worktreeDir = tmpDir
		// git worktree add 要求目标目录不存在或为空。
		if err := os.Remove(worktreeDir); err != nil {
			return nil, fmt.Errorf("prepare worktree path: %w", err)
		}
		if out, err := b.runGit(buildCtx, repoDir, "worktree", "add", "--detach", worktreeDir, ref); err != nil {
			return nil, fmt.Errorf("git worktree add %s: %w: %s", ref, err, out)
		}
		defer func() {
			_, _ = b.runGit(context.WithoutCancel(ctx), repoDir, "worktree", "remove", "--force", worktreeDir)
			_ = os.RemoveAll(worktreeDir)
		}()
		buildDir = worktreeDir
	}

	pkg := strings.TrimSpace(req.Package)
	if pkg == "" {
		pkg = "./cmd/gateway"
	}

	outPath := strings.TrimSpace(req.Output)
	if outPath == "" {
		dir, err := os.MkdirTemp("", "llmgw-build-out-")
		if err != nil {
			return nil, fmt.Errorf("create output dir: %w", err)
		}
		outPath = filepath.Join(dir, "llm-gateway-go")
	}

	buildOut, err := b.run(buildCtx, buildDir, "build", "-trimpath", "-o", outPath, pkg)
	if err != nil {
		return nil, fmt.Errorf("go build %s: %w: %s", pkg, err, buildOut)
	}

	// 版本元数据：优先用请求里给的，其次用源码树里的 version.json。
	version, buildSeq, gitSHA, versionJSON, err := b.resolveVersion(buildDir, req)
	if err != nil {
		return nil, err
	}

	return &SourceBuildResult{
		BinaryPath:   outPath,
		Version:      version,
		BuildSeq:     buildSeq,
		GitSHA:       gitSHA,
		VersionJSON:  versionJSON,
		DurationMs:   time.Since(start).Milliseconds(),
		UsedWorktree: worktreeDir != "",
	}, nil
}

// versionInfoShape 是 version.json 的字段集合（与 cmd/gateway/version.go 保持一致）。
type versionInfoShape struct {
	Version   string `json:"version"`
	GitTag    string `json:"git_tag"`
	GitSHA    string `json:"git_sha"`
	BuildSeq  int    `json:"build_seq"`
	BuildDate string `json:"build_date"`
	Module    string `json:"module"`
}

// resolveVersion 决定编译产物的版本元数据。
func (b *SourceBuilder) resolveVersion(buildDir string, req SourceBuildRequest) (string, int, string, []byte, error) {
	info := versionInfoShape{
		Version:   "dev",
		GitTag:    "v0.0.0",
		GitSHA:    "unknown",
		BuildDate: time.Now().UTC().Format("20060102"),
		Module:    "llm-gateway-go",
	}
	if raw, err := os.ReadFile(filepath.Join(buildDir, "version.json")); err == nil {
		var parsed versionInfoShape
		if err := json.Unmarshal(raw, &parsed); err == nil {
			if parsed.Version != "" {
				info = parsed
			}
		}
	}

	if v := strings.TrimSpace(req.Version); v != "" {
		info.Version = v
	}
	if req.BuildSeq > 0 {
		info.BuildSeq = req.BuildSeq
	}
	if info.Version == "" {
		info.Version = "dev"
	}

	encoded, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return "", 0, "", nil, fmt.Errorf("encode version metadata: %w", err)
	}
	return info.Version, info.BuildSeq, info.GitSHA, append(encoded, '\n'), nil
}

// run 在指定目录执行 go 命令并返回合并输出。
func (b *SourceBuilder) run(ctx context.Context, dir string, args ...string) (string, error) {
	goBin := b.goBin
	if goBin == "" {
		goBin = "go"
	}
	return b.runBinary(ctx, dir, goBin, args...)
}

// runGit 在指定目录执行 git 命令并返回合并输出。
//
// ★ git 与 go 必须走不同的可执行文件：早期版本把两者都塞进同一个 run()，
//
//	结果 `git worktree add` 被执行成 `go worktree add` —— 报「unknown command」。
//	这种错只有真跑一次源码构建才会暴露，单元测试（只测错误路径）抓不到。
func (b *SourceBuilder) runGit(ctx context.Context, dir string, args ...string) (string, error) {
	gitBin := b.gitBin
	if gitBin == "" {
		gitBin = "git"
	}
	return b.runBinary(ctx, dir, gitBin, args...)
}

// runBinary 执行指定可执行文件并返回合并输出。
func (b *SourceBuilder) runBinary(ctx context.Context, dir, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	// 编译不能被交互式 stdin 卡住。
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	return string(out), err
}
