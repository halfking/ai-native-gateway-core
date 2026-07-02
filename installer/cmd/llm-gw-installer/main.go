// llm-gw-installer - llm-gateway-go 跨平台一键安装器
//
//	llm-gw-installer doctor      检测环境
//	llm-gw-installer install     安装并部署
//	llm-gw-installer uninstall   卸载
package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
var sqlTmpDir = ""

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

	// 1. 环境检测
	osInfo, err := envdetect.Detect()
	if err != nil {
		return fmt.Errorf("环境检测失败: %w", err)
	}
	logStep("1/9", "环境检测")
	fmt.Printf("  ✅ %s (%s) | %s | %s\n",
		osInfo.Distribution, osInfo.Arch, osInfo.PackageMgr, osInfo.ContainerEng)
	if osInfo.ContainerEng == envdetect.EngNone {
		logInfo("  ⚠️  Docker 未安装，尝试自动安装 ...")
		strategy := envdetect.PlanInstall(osInfo, true)
		if err := strategy.Execute(osInfo, logInfo); err != nil {
			return fmt.Errorf("Docker 安装失败: %w", err)
		}
		// 重新探测
		osInfo.ContainerEng, osInfo.DockerVersion = envdetect.Detect().ContainerEng, envdetect.Detect().DockerVersion
	}

	// 2. 前置条件
	prereq, _ := envdetect.CheckPrereq([]int{8781, 5432, 6379})
	if !prereq.PortsOK {
		logWarn(fmt.Sprintf("端口被占用: %v（可能需要修改配置）", prereq.PortsInUse))
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

	// 4. 配置向导
	logStep("2/9", "配置向导")
	imageTag := readAppImageTag()
	wiz := prompt.NewWizard(imageTag)
	cfg, err := wiz.Run(installDir)
	if err != nil {
		return fmt.Errorf("配置失败: %w", err)
	}
	cfg.InstallPath = installDir

	// 5. 加载/拉取镜像（4 层 fallback）
	logStep("3/9", "加载/拉取 Docker 镜像")
	strategy := imgsrc.NewDefaultStrategy(installDir, imgsrc.LoadRegistryFromEnv(), imgsrc.LoadRegistryAuthFromEnv())

	images := []imgsrc.ImageSpec{
		{Name: "kx-llm-gateway-go", Tag: cfg.AppImageTag},
		{Name: "kx-citus", Tag: "v11.3.0"},        // 重打 tag 后用 kx-citus
		{Name: "kx-redis", Tag: "v7-alpine"},      // 重打 tag 后用 kx-redis
	}
	for _, img := range images {
		if err := strategy.Pull(img, logInfo); err != nil {
			return fmt.Errorf("拉取镜像失败: %w", err)
		}
	}
	// 重打 citus/redis 的 tag 以便 compose 引用
	reTagForCompose()

	// 6. 写入 .env
	logStep("4/9", "生成 .env")
	env := secrets.NewEnvFile(filepath.Join(installDir, ".env"))
	if err := env.Write(envEntries(cfg)); err != nil {
		return fmt.Errorf("写入 .env 失败: %w", err)
	}
	logInfo("  ✅ .env (chmod 600, LF no BOM)")

	// 7. 创建容器外目录
	logStep("5/9", "创建持久化目录")
	for _, dir := range []string{"volumes/attachments", "volumes/logs"} {
		full := filepath.Join(installDir, dir)
		if err := os.MkdirAll(full, 0755); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}
	}
	logInfo("  ✅ volumes/{attachments,logs}")

	// 8. 写入 compose.yml
	logStep("6/9", "写入 compose.yml")
	composePath := filepath.Join(installDir, "compose.yml")
	if err := os.WriteFile(composePath, composeYAML, 0644); err != nil {
		return fmt.Errorf("写入 compose.yml 失败: %w", err)
	}
	logInfo("  ✅ compose.yml")

	// 9. 启动容器
	logStep("7/9", "启动 Docker 容器")
	compose := dockerutil.NewCompose(composePath, "llm-gateway-go", installDir)
	if err := compose.Up(logInfo); err != nil {
		return fmt.Errorf("启动失败: %w", err)
	}

	// 10. 初始化数据库
	logStep("8/9", "初始化数据库")
	dbRunner := dbinit.NewRunner("kx-citus", "kxuser", "llm_gateway", setupSQLDir())
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
		logWarn("⚠️  --purge 模式：删除数据卷")
		for _, dir := range []string{"volumes/attachments", "volumes/logs"} {
			os.RemoveAll(filepath.Join(installDir, dir))
		}
		os.Remove(filepath.Join(installDir, ".env"))
		logInfo("✅ 已彻底清理")
	} else {
		logInfo("✅ 已停止（数据保留）")
	}
	return nil
}

// ── 辅助函数 ────────────────────────────────────────────────────

func printBanner() {
	fmt.Println()
	fmt.Println("╔════════════════════════════════════════════════════════════════╗")
	fmt.Println("║  LLM Gateway 一键安装器 v" + installerVersion + strings.Repeat(" ", 39-len(installerVersion)) + "║")
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
	// 尝试读 MANIFEST.json
	data, err := os.ReadFile("MANIFEST.json")
	if err == nil {
		// 简单解析（不用 JSON 库以减少依赖）
		content := string(data)
		idx := strings.Index(content, `"tag"`)
		if idx > 0 {
			// 找到 "tag": "v1.0.0"
			line := content[idx:idx+100]
			if start := strings.Index(line, `"`); start > 0 {
				if end := strings.Index(line[start+1:], `"`); end > 0 {
					return line[start+1 : start+1+end]
				}
			}
		}
	}
	return "latest"
}

// reTagForCompose 重打 citus/redis tag 为 compose 引用的名字
// 这样 compose.yml 里写 kx-citus:v11.3.0 / kx-redis:v7-alpine 就能找到镜像
func reTagForCompose() {
	pairs := []struct{ src, dst string }{
		{"citusdata/citus:11.3.0", "kx-citus:v11.3.0"},
		{"redis:7-alpine", "kx-redis:v7-alpine"},
	}
	for _, p := range pairs {
		// 检查 src 是否存在，存在才 tag
		cmd := exec.Command("docker", "image", "inspect", p.src)
		if err := cmd.Run(); err == nil {
			_ = exec.Command("docker", "tag", p.src, p.dst).Run()
		}
	}
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

// setupSQLDir 把 embed 的 SQL 写到临时目录，返回目录路径
func setupSQLDir() string {
	if sqlTmpDir != "" {
		return sqlTmpDir
	}
	tmp, err := os.MkdirTemp("", "llm-gw-sql-")
	if err != nil {
		return "sql"
	}
	files := map[string][]byte{
		"00-prereqs.sql": sqlPrereqs,
		"01-schema.sql":  sqlSchema,
		"02-seed.sql":    sqlSeed,
	}
	for name, content := range files {
		path := filepath.Join(tmp, name)
		if err := os.WriteFile(path, content, 0644); err != nil {
			return "sql"
		}
	}
	sqlTmpDir = tmp
	return sqlTmpDir
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
