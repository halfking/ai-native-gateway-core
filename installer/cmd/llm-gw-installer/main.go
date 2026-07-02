// llm-gw-installer - llm-gateway-go 跨平台一键安装器
//
//	llm-gw-installer doctor      检测环境
//	llm-gw-installer install     安装并部署
//	llm-gw-installer uninstall   卸载
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dbinit"
	"github.com/kaixuan/llm-gateway-go/installer/internal/dockerutil"
	"github.com/kaixuan/llm-gateway-go/installer/internal/envdetect"
	"github.com/kaixuan/llm-gateway-go/installer/internal/imgsrc"
	"github.com/kaixuan/llm-gateway-go/installer/internal/prompt"
	"github.com/kaixuan/llm-gateway-go/installer/internal/report"
	"github.com/kaixuan/llm-gateway-go/installer/internal/secrets"
)

const installerVersion = "1.0.0"

// ── 内嵌资源（go:embed） ────────────────────────────────────────
// go:embed 路径必须在当前文件同目录或子目录下
// 所以把资源放在 embeddata/ 目录里

//go:embed embeddata/compose.yml
var composeYAML []byte

//go:embed embeddata/env.template
var envTemplate []byte

//go:embed embeddata/install-report.md.tmpl
var reportTemplate []byte

//go:embed embeddata/00-prereqs.sql
var sqlPrereqs []byte

//go:embed embeddata/01-schema.sql
var sqlSchema []byte

//go:embed embeddata/02-seed.sql
var sqlSeed []byte

// 临时存放 embed SQL 的目录（运行时写入）

// ── Cobra 入口 ──────────────────────────────────────────────────

func main() {
	root := &cobra.Command{
		Use:   "llm-gw-installer",
		Short: "llm-gateway-go 跨平台一键安装器",
		Long: fmt.Sprintf(`llm-gw-installer v%s

为客户机器提供零依赖、跨平台的一键部署体验。
支持 Windows / Linux / macOS / 国产 OS / 国产 CPU。
内置 4 层镜像源兜底（离线包 → 内部 registry → 国内 mirror → 官方源）。`,
			installerVersion),
	}

	root.AddCommand(doctorCmd())
	root.AddCommand(installCmd())
	root.AddCommand(uninstallCmd())
	root.AddCommand(versionCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}

// ── doctor 子命令 ──────────────────────────────────────────────

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "检测当前环境",
		RunE:  runDoctor,
	}
}

func runDoctor(cmd *cobra.Command, args []string) error {
	printBanner()

	fmt.Println("▶ 环境检测 ...")

	osInfo, err := envdetect.Detect()
	if err != nil {
		return fmt.Errorf("检测 OS 失败: %w", err)
	}

	fmt.Printf("  ✅ OS:        %s (%s)\n", osInfo.Distribution, osInfo.Arch)
	fmt.Printf("  ✅ 内核:      %s\n", osInfo.Kernel)
	fmt.Printf("  ✅ 包管理器:  %s\n", osInfo.PackageMgr)
	if osInfo.IsChineseOS() {
		fmt.Printf("  ℹ️  国产 OS:  是\n")
	}
	fmt.Printf("  ✅ 容器引擎:  %s %s\n", osInfo.ContainerEng, osInfo.DockerVersion)
	fmt.Printf("  ✅ sudo:      %v\n", osInfo.HasSudo)

	fmt.Println("\n▶ 网络探测 ...")
	net := envdetect.ProbeNetwork()
	fmt.Printf("  %s registry.kxpms.cn\n", boolMark(net.InternalRegistryOK))
	fmt.Printf("  %s aliyun mirror\n", boolMark(net.AliyunMirrorOK))
	fmt.Printf("  %s docker.io\n", boolMark(net.DockerHubOK))

	fmt.Println("\n▶ 前置条件 ...")
	prereq, _ := envdetect.CheckPrereq([]int{8781, 5432, 6379})
	fmt.Printf("  %s 端口空闲: %v\n", boolMark(prereq.PortsOK), prereq.PortsInUse)
	if !prereq.PortsOK {
		for _, port := range prereq.PortsInUse {
			if proc, ok := prereq.PortDetails[port]; ok {
				fmt.Printf("     端口 %d: %s\n", port, proc)
			}
		}
	}
	fmt.Printf("  ✅ 磁盘:     %d GB\n", prereq.DiskFreeGB)
	fmt.Printf("  ✅ 内存:     %d MB\n", prereq.RAMMB)

	return nil
}

// ── install 子命令 ─────────────────────────────────────────────

func installCmd() *cobra.Command {
	var (
		skipDoctor   bool
		skipPrompt   bool
		installDir   string
		configFile   string
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "一键安装并部署 llm-gateway-go",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(installOpts{
				SkipDoctor: skipDoctor,
				SkipPrompt: skipPrompt,
				InstallDir: installDir,
				ConfigFile: configFile,
			})
		},
	}

	cmd.Flags().BoolVar(&skipDoctor, "skip-doctor", false, "跳过环境检测")
	cmd.Flags().BoolVar(&skipPrompt, "skip-prompt", false, "跳过交互（需提供 --config）")
	cmd.Flags().StringVar(&installDir, "dir", "", "安装目录（默认当前目录）")
	cmd.Flags().StringVar(&configFile, "config", "", "配置文件路径（跳过交互）")

	return cmd
}

type installOpts struct {
	SkipDoctor bool
	SkipPrompt bool
	InstallDir string
	ConfigFile string
}

func runInstall(opts installOpts) error {
	printBanner()

	var osInfo *envdetect.OSInfo
	var err error

	// 1. 环境检测（--skip-doctor 可跳过）
	if opts.SkipDoctor {
		logStep("1/9", "环境检测（已跳过）")
		osInfo, err = envdetect.Detect()
		if err != nil {
			return fmt.Errorf("基础环境检测失败: %w", err)
		}
		goto prereqCheck
	}

	logStep("1/9", "环境检测")
	osInfo, err = envdetect.Detect()
	if err != nil {
		return fmt.Errorf("环境检测失败: %w", err)
	}
	fmt.Printf("  ✅ %s (%s) | %s | %s\n",
		osInfo.Distribution, osInfo.Arch, osInfo.PackageMgr, osInfo.ContainerEng)
	if osInfo.ContainerEng == envdetect.EngNone {
		logInfo("  ⚠️  Docker 未安装，尝试自动安装 ...")
		strategy := envdetect.PlanInstall(osInfo, true)
		if err := strategy.Execute(osInfo, logInfo); err != nil {
			return fmt.Errorf("Docker 安装失败: %w", err)
		}
		// 重新探测：必须成功，否则报错（吞错会导致后续步骤 100% 失败）
		newInfo, err := envdetect.Detect()
		if err != nil {
			return fmt.Errorf("Docker 安装后重新探测失败: %w", err)
		}
		if newInfo == nil || newInfo.ContainerEng == envdetect.EngNone {
			return fmt.Errorf("Docker 安装后仍检测不到容器引擎，请手动验证后重跑")
		}
		osInfo.ContainerEng = newInfo.ContainerEng
		osInfo.DockerVersion = newInfo.DockerVersion
	}

	// 2. 前置条件
prereqCheck:
	prereq, _ := envdetect.CheckPrereq([]int{8781, 5432, 6379})
	if !prereq.PortsOK {
		msg := fmt.Sprintf("端口被占用: %v（可能需要修改配置）", prereq.PortsInUse)
		for _, port := range prereq.PortsInUse {
			if proc, ok := prereq.PortDetails[port]; ok {
				msg += fmt.Sprintf("\n  端口 %d: %s", port, proc)
			}
		}
		logWarn(msg)
	}
	if prereq.DiskFreeGB < 5 {
		return fmt.Errorf("磁盘空间不足: %d GB (需要至少 5 GB)", prereq.DiskFreeGB)
	}

	// 3. 准备安装目录
	installDir := opts.InstallDir
	if installDir == "" {
		installDir, _ = os.Getwd()
	}
	if err := os.Chdir(installDir); err != nil {
		return fmt.Errorf("切换到安装目录失败: %w", err)
	}

	// 4. 配置：交互向导 或 从文件加载（--config）/ 自动生成（--skip-prompt）
	logStep("2/9", "配置")
	imageTag := readAppImageTag()
	var cfg *prompt.InstallConfig
	if opts.ConfigFile != "" {
		// 从配置文件加载（CI 自动化场景）
		logInfo(fmt.Sprintf("  ▶ 从 %s 加载配置 ...", opts.ConfigFile))
		cfg, err = prompt.LoadFromEnvFile(opts.ConfigFile, imageTag, installDir)
		if err != nil {
			return fmt.Errorf("加载配置失败: %w", err)
		}
		logInfo("  ✅ 配置已加载（缺失的 secrets 已自动生成）")
	} else if opts.SkipPrompt {
		// 跳过交互：全部用默认值 + 自动生成 secrets
		logInfo("  ▶ --skip-prompt 模式：使用默认配置 + 自动生成 secrets")
		cfg, err = prompt.LoadFromEnvFile("", imageTag, installDir)
		if err != nil {
			return fmt.Errorf("生成默认配置失败: %w", err)
		}
		logInfo("  ✅ 默认配置已生成")
	} else {
		// 交互向导
		wiz := prompt.NewWizard(imageTag)
		cfg, err = wiz.Run(installDir)
		if err != nil {
			return fmt.Errorf("配置失败: %w", err)
		}
	}
	cfg.InstallPath = installDir

	// 5. 加载/拉取镜像（4 层 fallback）
	// citus/redis 用上游原始名拉取（公网才有），成功后自动 retag 成 compose.yml 引用的 kx-* 名
	logStep("3/9", "加载/拉取 Docker 镜像")
	strategy := imgsrc.NewDefaultStrategy(installDir, imgsrc.LoadRegistryFromEnv(), imgsrc.LoadRegistryAuthFromEnv())

	// image: 实际拉取的镜像（原始名，公网可达）
	// alias: compose.yml 里引用的名字（拉取成功后自动 docker tag）
	type pullItem struct {
		spec  imgsrc.ImageSpec
		alias string
	}
	items := []pullItem{
		{spec: imgsrc.ImageSpec{Name: "kx-llm-gateway-go", Tag: cfg.AppImageTag}, alias: "kx-llm-gateway-go:latest"},
		{spec: imgsrc.ImageSpec{Name: "citusdata/citus", Tag: "11.3.0"}, alias: "kx-citus:v11.3.0"},
		{spec: imgsrc.ImageSpec{Name: "redis", Tag: "7-alpine"}, alias: "kx-redis:v7-alpine"},
	}
	for _, it := range items {
		if err := strategy.PullWithAlias(it.spec, it.alias, logInfo); err != nil {
			return fmt.Errorf("拉取镜像失败: %w", err)
		}
	}

	// 6. 写入 .env
	logStep("4/9", "生成 .env")
	env := secrets.NewEnvFile(filepath.Join(installDir, ".env"))
	if err := env.Write(envEntries(cfg)); err != nil {
		return fmt.Errorf("写入 .env 失败: %w", err)
	}
	logInfo("  ✅ .env (chmod 600, LF no BOM)")

	// 7. 创建容器外目录结构
	logStep("5/9", "创建持久化目录结构")
	if err := createDirectoryLayout(installDir); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	logInfo("  ✅ 9 个子目录创建完成")

	// 8. 写入 compose.yml + VERSION + 复制 installer 副本
	logStep("6/9", "写入配置文件")
	composePath := filepath.Join(installDir, "compose.yml")
	if err := os.WriteFile(composePath, composeYAML, 0644); err != nil {
		return fmt.Errorf("写入 compose.yml 失败: %w", err)
	}
	logInfo("  ✅ compose.yml")

	// 8a. 写入 app/VERSION
	versionPath := filepath.Join(installDir, "app", "VERSION")
	if err := os.WriteFile(versionPath, []byte(cfg.AppImageTag+"\n"), 0644); err != nil {
		logInfo("  ⚠️  写入 VERSION 失败: " + err.Error())
	} else {
		logInfo("  ✅ app/VERSION")
	}

	// 8b. 复制当前 installer 到 bin/
	binDir := filepath.Join(installDir, "bin")
	if err := copyInstallerSelf(binDir); err != nil {
		logInfo("  ⚠️  复制 installer 副本失败: " + err.Error())
	} else {
		logInfo("  ✅ bin/llm-gw-installer")
	}

	// 8c. 复制 SQL 备份到 db/init/
	if err := copySQLBackup(installDir); err != nil {
		logInfo("  ⚠️  复制 SQL 备份失败: " + err.Error())
	} else {
		logInfo("  ✅ db/init/*.sql")
	}

	// 8d. 复制 MANIFEST.json 到 config/
	if err := copyManifest(installDir); err != nil {
		logInfo("  ⚠️  复制 MANIFEST 失败: " + err.Error())
	} else {
		logInfo("  ✅ config/MANIFEST.json")
	}

	// 9. 启动容器
	logStep("7/9", "启动 Docker 容器")
	compose := dockerutil.NewCompose(composePath, "llm-gateway-go", installDir)
	if err := compose.Up(logInfo); err != nil {
		return fmt.Errorf("启动失败: %w", err)
	}

	// 10. 初始化数据库
	logStep("8/9", "初始化数据库")
	sqlDir, sqlCleanup, err := setupSQLDir()
	if err != nil {
		return fmt.Errorf("准备 SQL 文件失败: %w", err)
	}
	defer sqlCleanup()
	dbRunner := dbinit.NewRunner("kx-citus", "kxuser", "llm_gateway", sqlDir)
	if err := dbRunner.WaitForPG(60); err != nil {
		return fmt.Errorf("等待 PG: %w", err)
	}
	if err := dbRunner.InitSchema(logInfo); err != nil {
		return fmt.Errorf("初始化 DB 失败: %w", err)
	}

	// 11. 健康检查
	logStep("9/9", "健康检查")
	hc := dockerutil.NewHealthChecker(cfg.AppPort, "kx-citus", "kx-redis", cfg.RedisPassword, "kxuser", "llm_gateway")
	health, _ := hc.RunAll(logInfo)

	// 12. 写入报告
	reportPath := filepath.Join(installDir, "install-report.md")
	reportData := &report.InstallReportData{
		InstallerVersion: installerVersion,
		InstallTime:      report.Now(),
		OSInfo:           osInfo,
		Config:           cfg,
		AppImageTag:      cfg.AppImageTag,
		ImageSources: report.ImageSourceSummary{
			App:   "loaded/pulled (see log)",
			Citus: "loaded/pulled (see log)",
			Redis: "loaded/pulled (see log)",
		},
		Health: health,
	}
	if err := report.Write(reportPath, reportData, string(reportTemplate)); err != nil {
		logWarn(fmt.Sprintf("写入报告失败: %v", err))
	}

	// 输出总结
	printSummary(cfg, health)

	if !health.AllOK() {
		return fmt.Errorf("健康检查未全部通过，请查看 install-report.md")
	}
	return nil
}

// ── version 子命令 ─────────────────────────────────────────────

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "显示 installer 版本",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("llm-gw-installer v%s\n", installerVersion)
			fmt.Printf("Go: %s\n", runtime.Version())
			fmt.Printf("OS/Arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
		},
	}
}

// ── uninstall 子命令 ───────────────────────────────────────────

func uninstallCmd() *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "卸载 llm-gateway-go",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUninstall(purge)
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "同时删除数据卷")
	return cmd
}

func runUninstall(purge bool) error {
	installDir, _ := os.Getwd()
	composePath := filepath.Join(installDir, "compose.yml")

	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		return fmt.Errorf("当前目录未发现 compose.yml，请 cd 到安装目录")
	}

	compose := dockerutil.NewCompose(composePath, "llm-gateway-go", installDir)
	if err := compose.Down(logInfo, purge); err != nil {
		return err
	}

	if purge {
		logWarn("⚠️  --purge 模式：删除所有持久化数据")
		// 按新的目录结构（bind-mount 到容器外的实际路径）
		purgeDirs := []string{
			"db/data",      // PostgreSQL 数据
			"redis/data",   // Redis 数据
			"attachments",  // 应用附件
			"app/logs",     // 应用日志
		}
		for _, dir := range purgeDirs {
			full := filepath.Join(installDir, dir)
			if err := os.RemoveAll(full); err != nil {
				logWarn(fmt.Sprintf("  删除 %s 失败: %v", dir, err))
			}
		}
		os.Remove(filepath.Join(installDir, ".env"))
		logInfo("✅ 已彻底清理（数据库、Redis、附件、日志、.env）")
	} else {
		logInfo("✅ 已停止（数据保留在 db/data、redis/data、attachments、app/logs）")
	}
	return nil
}

// ── 辅助函数 ────────────────────────────────────────────────────

func printBanner() {
	fmt.Println()
	fmt.Println("╔════════════════════════════════════════════════════════════════╗")
	// 自适应对齐：version 过长时截断，避免 strings.Repeat 负数 panic
	pad := 39 - len(installerVersion)
	if pad < 1 {
		pad = 1
	}
	fmt.Println("║  LLM Gateway 一键安装器 v" + installerVersion + strings.Repeat(" ", pad) + "║")
	fmt.Println("╚════════════════════════════════════════════════════════════════╝")
	fmt.Println()
}

func logStep(step, name string) {
	fmt.Println()
	fmt.Printf("▶ [%s] %s\n", step, name)
}

func logInfo(msg string) {
	fmt.Println(msg)
}

func logWarn(msg string) {
	fmt.Println("⚠️  " + msg)
}

func boolMark(b bool) string {
	if b {
		return "✅"
	}
	return "❌"
}

// readAppImageTag 读取应用镜像 tag（从 MANIFEST.json 或环境变量）
func readAppImageTag() string {
	if t := os.Getenv("APP_IMAGE_TAG"); t != "" {
		return t
	}
	// 尝试读 MANIFEST.json（用标准库 json 解析，避免手动字符串匹配的 bug）
	data, err := os.ReadFile("MANIFEST.json")
	if err == nil {
		var manifest struct {
			Images []struct {
				Name string `json:"name"`
				Tag  string `json:"tag"`
			} `json:"images"`
		}
		if json.Unmarshal(data, &manifest) == nil {
			for _, img := range manifest.Images {
				if img.Name == "kx-llm-gateway-go" && img.Tag != "" {
					return img.Tag
				}
			}
		}
	}
	return "latest"
}

// ── 目录结构相关 ─────────────────────────────────────────────────

// DirectoryLayout 客户机器上的部署目录结构
//
//	~/llm-gateway/
//	├── README.md              部署说明
//	├── .env                   所有 secrets（chmod 600）
//	├── compose.yml            docker-compose 全栈定义
//	├── install.sh / .ps1      入口脚本
//	├── uninstall.sh
//	├── bin/                   installer 副本（自更新用）
//	│   └── llm-gw-installer
//	├── config/                静态配置（git 可追踪）
//	│   ├── MANIFEST.json
//	│   └── env.template
//	├── app/                   应用相关
//	│   ├── VERSION
//	│   └── logs/              bind-mount → /var/log/llm-gateway
//	├── db/                    PostgreSQL
//	│   ├── data/              bind-mount → /var/lib/postgresql/data
//	│   ├── init/              SQL 初始化文件备份
//	│   │   ├── 00-prereqs.sql
//	│   ├── 01-schema.sql
//	│   └── 02-seed.sql
//	├── redis/                 Redis
//	│   └── data/              bind-mount → /data
//	├── attachments/           bind-mount → /opt/llm-gateway-go/data/attachments
//	├── backups/               备份根目录
//	│   ├── daily/
//	│   └── manual/
//	└── reports/               部署/运行报告
//	    └── install-report.md
type DirectoryLayout struct {
	Root string
}

// createDirectoryLayout 创建完整的目录结构
func createDirectoryLayout(root string) error {
	layout := DirectoryLayout{Root: root}

	dirs := []string{
		// 一级目录
		"bin",
		"config",
		"app",
		"app/logs",
		"db",
		"db/data",
		"db/init",
		"redis",
		"redis/data",
		"attachments",
		"backups",
		"backups/daily",
		"backups/manual",
		"reports",
	}

	for _, d := range dirs {
		full := filepath.Join(root, d)
		if err := os.MkdirAll(full, 0755); err != nil {
			return fmt.Errorf("创建 %s 失败: %w", d, err)
		}
	}

	// 写入每个目录的 .gitkeep（防止空目录被 git 忽略）
	for _, d := range dirs {
		gitkeep := filepath.Join(root, d, ".gitkeep")
		_ = os.WriteFile(gitkeep, []byte("# 持久化目录，请勿删除\n"), 0644)
	}

	// 写入 README.md 解释目录结构
	readme := layout.GenerateReadme()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(readme), 0644); err != nil {
		return err
	}

	return nil
}

// GenerateReadme 生成目录结构说明
func (l DirectoryLayout) GenerateReadme() string {
	return fmt.Sprintf(`# llm-gateway-go 部署目录

本目录包含 llm-gateway-go 的全部部署内容，所有数据均在容器外持久化。

## 目录结构

| 目录 | 用途 | 容器内路径 |
|------|------|------------|
| `+"`./bin/`"+` | installer 副本（自更新用） | - |
| `+"`./config/`"+` | 静态配置（MANIFEST/env 模板） | - |
| `+"`./app/`"+` | 应用版本文件 | - |
| `+"`./app/logs/`"+` | ⭐ 应用日志 | /var/log/llm-gateway |
| `+"`./db/data/`"+` | ⭐ PostgreSQL 数据 | /var/lib/postgresql/data |
| `+"`./db/init/`"+` | SQL 初始化文件备份 | - |
| `+"`./redis/data/`"+` | ⭐ Redis 数据 | /data |
| `+"`./attachments/`"+` | ⭐ 应用附件 | /opt/llm-gateway-go/data/attachments |
| `+"`./backups/`"+` | 全栈备份（pg_dump 等） | - |
| `+"`./reports/`"+` | 部署报告 | - |

⭐ = bind-mount，容器重启数据不丢失

## 数据持久化保证

所有关键数据（数据库、Redis、附件、日志）都通过 bind-mount 映射到此目录下：

- **容器删除/重建**：数据完整保留
- **Docker 重启**：数据完整保留
- **机器重启**：数据完整保留
- **卸载（--purge）**：删除此目录后才会清理

## 备份建议

定期备份以下目录：
- `+"`./db/data/`"+` （最关键，包含全部业务数据）
- `+"`./redis/data/`"+` （缓存数据）
- `+"`./attachments/`"+` （用户上传文件）
- `+"`./.env`"+` （所有 secrets）

或者运行 `+"`./bin/llm-gw-installer backup`"+`（如已实现）。
`)
}

// copyInstallerSelf 复制当前运行的 installer 副本到 bin/
func copyInstallerSelf(binDir string) error {
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return err
	}

	selfPath, err := os.Executable()
	if err != nil {
		return err
	}

	dst := filepath.Join(binDir, "llm-gw-installer")
	// Windows 加 .exe 后缀
	if runtime.GOOS == "windows" {
		dst += ".exe"
	}

	// 读取自身二进制
	data, err := os.ReadFile(selfPath)
	if err != nil {
		return err
	}

	// 写入目标位置
	mode := os.FileMode(0755)
	if err := os.WriteFile(dst, data, mode); err != nil {
		return err
	}

	return nil
}

// copySQLBackup 复制 embed SQL 到 db/init/
func copySQLBackup(root string) error {
	initDir := filepath.Join(root, "db", "init")
	if err := os.MkdirAll(initDir, 0755); err != nil {
		return err
	}

	files := map[string][]byte{
		"00-prereqs.sql": sqlPrereqs,
		"01-schema.sql":  sqlSchema,
		"02-seed.sql":    sqlSeed,
	}
	for name, content := range files {
		path := filepath.Join(initDir, name)
		if err := os.WriteFile(path, content, 0644); err != nil {
			return err
		}
	}
	return nil
}

// copyManifest 复制 MANIFEST.json 到 config/
func copyManifest(root string) error {
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}

	// 如果存在 MANIFEST.json（从 release 包带来），复制
	srcManifest := filepath.Join(root, "MANIFEST.json")
	if data, err := os.ReadFile(srcManifest); err == nil {
		dst := filepath.Join(configDir, "MANIFEST.json")
		return os.WriteFile(dst, data, 0644)
	}

	// 否则生成一个简化的
	manifest := fmt.Sprintf(`{
  "version": "%s",
  "installed_at": "%s",
  "installer_version": "%s"
}
`, os.Getenv("APP_IMAGE_TAG"), report.Now(), installerVersion)

	return os.WriteFile(filepath.Join(configDir, "MANIFEST.json"), []byte(manifest), 0644)
}

// envEntries 构造 .env 条目
func envEntries(cfg *prompt.InstallConfig) map[string]string {
	return map[string]string{
		"APP_IMAGE_TAG":                       cfg.AppImageTag,
		"PG_PORT":                             strconvItoa(cfg.PGPort),
		"POSTGRES_PASSWORD":                   cfg.PGPassword,
		"REDIS_PORT":                          strconvItoa(cfg.RedisPort),
		"REDIS_PASSWORD":                      cfg.RedisPassword,
		"APP_PORT":                            strconvItoa(cfg.AppPort),
		"LLM_GATEWAY_API_KEY":                 cfg.APIKey,
		"LLM_GATEWAY_ADMIN_API_KEY":           cfg.AdminAPIKey,
		"LLM_GATEWAY_JWT_SECRET":              cfg.JWTSecret,
		"LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY": cfg.CredEncryptKey,
	}
}

func strconvItoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// setupSQLDir 把 embed 的 SQL 写到临时目录，返回目录路径和清理函数。
// 调用方必须 defer 调用 cleanup 以释放临时目录。
func setupSQLDir() (string, func(), error) {
	tmp, err := os.MkdirTemp("", "llm-gw-sql-")
	if err != nil {
		return "", nil, fmt.Errorf("创建临时目录失败: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	files := map[string][]byte{
		"00-prereqs.sql": sqlPrereqs,
		"01-schema.sql":  sqlSchema,
		"02-seed.sql":    sqlSeed,
	}
	for name, content := range files {
		path := filepath.Join(tmp, name)
		if err := os.WriteFile(path, content, 0644); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("写入 %s 失败: %w", name, err)
		}
	}
	return tmp, cleanup, nil
}

func printSummary(cfg *prompt.InstallConfig, health *dockerutil.HealthReport) {
	fmt.Println()
	fmt.Println("╔════════════════════════════════════════════════════════════════╗")
	fmt.Println("║  ✅ 部署完成                                                  ║")
	fmt.Println("╠════════════════════════════════════════════════════════════════╣")
	fmt.Println("║                                                                ║")
	fmt.Printf("║  服务地址:    http://localhost:%-5d                            ║\n", cfg.AppPort)
	fmt.Printf("║  健康检查:    http://localhost:%-5d/healthz                    ║\n", cfg.AppPort)
	fmt.Printf("║                                                                ║\n")
	fmt.Printf("║  部署报告:    %s/install-report.md         ║\n", cfg.InstallPath)
	fmt.Printf("║  配置:        %s/.env (chmod 600)             ║\n", cfg.InstallPath)
	fmt.Printf("║                                                                ║\n")
	fmt.Printf("║  重启: docker compose -f %s/compose.yml restart     ║\n", cfg.InstallPath)
	fmt.Printf("║  日志: docker compose -f %s/compose.yml logs -f      ║\n", cfg.InstallPath)
	fmt.Printf("║  停止: docker compose -f %s/compose.yml down         ║\n", cfg.InstallPath)
	fmt.Printf("║  卸载: llm-gw-installer uninstall --purge                   ║\n")
	fmt.Println("║                                                                ║")
	fmt.Println("╚════════════════════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Println("健康检查:")
	fmt.Println(health.String())
}
