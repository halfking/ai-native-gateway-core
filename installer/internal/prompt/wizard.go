// Package prompt 提供前置交互向导（13 步配置）
package prompt

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/kaixuan/llm-gateway-go/installer/internal/secrets"
)

// InstallConfig 安装配置（从 wizard 收集）
type InstallConfig struct {
	InstallPath         string
	AppPort             int
	PGPort              int
	RedisPort           int
	AppImageTag         string
	PGPassword          string
	RedisPassword       string
	APIKey              string
	AdminAPIKey         string
	JWTSecret           string
	CredEncryptKey      string
	ImageSourceStrategy string // "auto" / "offline-only" / "registry-only"
	// StorageMode: ""（默认 full） / "full" / "lite"
	//   full = PG + Redis（kxpms 标准生产路径）
	//   lite = SQLite + 本地（单机/开发/CI）
	StorageMode string
	// MasterURL: 主控端 URL（用于 license 激活 / 心跳）
	MasterURL string
	// SkipActivation: 是否在 install 末尾跳过自动激活调用
	SkipActivation bool
}

// IsLite 是否为 lite 模式（SQLite + 本地）
func (c *InstallConfig) IsLite() bool {
	return strings.EqualFold(c.StorageMode, "lite")
}

// NormalizeStorageMode 规范化存储模式字符串：未知值归一为 full
func NormalizeStorageMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "lite":
		return "lite"
	case "", "full":
		return "full"
	default:
		return "full"
	}
}

// Summary 输出可读摘要
func (c *InstallConfig) Summary() string {
	mode := c.StorageMode
	if mode == "" {
		mode = "full"
	}
	return fmt.Sprintf(`  安装路径:    %s
  应用端口:    %d
  存储模式:    %s
  主控端 URL:  %s
  PG 端口:    %d
  Redis 端口:  %d
  镜像 tag:   %s
  镜像策略:   %s
  密码:       (全部自动生成，留空时填入)
  API Key:    (自动生成)
  JWT Secret: (自动生成)`,
		c.InstallPath, c.AppPort, mode, c.MasterURL,
		c.PGPort, c.RedisPort, c.AppImageTag, c.ImageSourceStrategy)
}

// Wizard 向导上下文
type Wizard struct {
	reader      *bufio.Reader
	IsTTY       bool
	AppImageTag string // 由 caller 传入（从 MANIFEST 读取）
}

// NewWizard 创建 Wizard
func NewWizard(appImageTag string) *Wizard {
	return &Wizard{
		reader:      bufio.NewReader(os.Stdin),
		IsTTY:       term.IsTerminal(int(os.Stdin.Fd())),
		AppImageTag: appImageTag,
	}
}

// LoadFromEnvFile 从 .env 风格的文件加载配置（非交互模式，CI/自动化场景）
// 文件格式：KEY=VALUE，每行一个，支持 # 注释
// path 为空时跳过文件读取，所有 secrets 字段通过闭包 gen 生成（返回 error 而非 panic）。
// 缺失的必填字段会自动生成（与交互模式留空语义一致）
func LoadFromEnvFile(path, appImageTag, defaultInstallPath string) (*InstallConfig, error) {
	var data []byte
	if path != "" {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
		}
	}

	values := parseEnvFile(string(data))

	// gen: 缺失则调用 secrets 生成随机值并返回 error（替代 panic）
	gen := func(key string) (string, error) {
		if v, ok := values[key]; ok && v != "" {
			return v, nil
		}
		v, err := secrets.GenerateRandom()
		if err != nil {
			return "", fmt.Errorf("生成随机 %s 失败: %w", key, err)
		}
		return v, nil
	}

	pgPwd, err := gen("POSTGRES_PASSWORD")
	if err != nil {
		return nil, err
	}
	redisPwd, err := gen("REDIS_PASSWORD")
	if err != nil {
		return nil, err
	}
	apiKey, err := gen("LLM_GATEWAY_API_KEY")
	if err != nil {
		return nil, err
	}
	adminKey, err := gen("LLM_GATEWAY_ADMIN_API_KEY")
	if err != nil {
		return nil, err
	}
	jwtSecret, err := gen("LLM_GATEWAY_JWT_SECRET")
	if err != nil {
		return nil, err
	}
	credKey, err := gen("LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY")
	if err != nil {
		return nil, err
	}

	cfg := &InstallConfig{
		InstallPath:         getOrDefault(values, "INSTALL_PATH", defaultInstallPath),
		AppPort:             getOrDefaultInt(values, "APP_PORT", 8781),
		PGPort:              getOrDefaultInt(values, "PG_PORT", 5432),
		RedisPort:           getOrDefaultInt(values, "REDIS_PORT", 6379),
		AppImageTag:         getOrDefault(values, "APP_IMAGE_TAG", appImageTag),
		PGPassword:          pgPwd,
		RedisPassword:       redisPwd,
		APIKey:              apiKey,
		AdminAPIKey:         adminKey,
		JWTSecret:           jwtSecret,
		CredEncryptKey:      credKey,
		ImageSourceStrategy: getOrDefault(values, "IMAGE_SOURCE_STRATEGY", "auto"),
		// 兼容两个键名：STORAGE_MODE（--config 自有键）与 LLM_GATEWAY_STORAGE_MODE
		// （installer 写出的 .env 键）。否则拿已有 .env 重装时 lite 会被静默重置为 full。
		StorageMode: NormalizeStorageMode(getOrDefault(values,
			"STORAGE_MODE", getOrDefault(values, "LLM_GATEWAY_STORAGE_MODE", "full"))),
		MasterURL:      getOrDefault(values, "LLM_GATEWAY_MASTER_URL", "https://llm.kxpms.cn"),
		SkipActivation: getOrDefaultBool(values, "INSTALL_SKIP_ACTIVATION", false),
	}
	return cfg, nil
}

// parseEnvFile 解析 .env 文件内容为 map
func parseEnvFile(content string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		// 去掉两端引号
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		result[key] = val
	}
	return result
}

func getOrDefault(m map[string]string, key, def string) string {
	if v, ok := m[key]; ok && v != "" {
		return v
	}
	return def
}

func getOrDefaultInt(m map[string]string, key string, def int) int {
	if v, ok := m[key]; ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getOrDefaultBool(m map[string]string, key string, def bool) bool {
	if v, ok := m[key]; ok && v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		}
	}
	return def
}

// Run 运行 13 步向导
func (w *Wizard) Run(defaultPath string) (*InstallConfig, error) {
	cfg := &InstallConfig{
		AppPort:             8781,
		PGPort:              5432,
		RedisPort:           6379,
		AppImageTag:         w.AppImageTag,
		ImageSourceStrategy: "auto",
		StorageMode:         "full",
		MasterURL:           "https://llm.kxpms.cn",
		SkipActivation:      false,
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  配置向导（共 13 步，留空将自动生成强随机值）")
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println()

	// 1. 安装路径
	cfg.InstallPath = w.askString("1. 安装路径", defaultPath)

	// 2. 存储模式（lite = SQLite+本地 / full = PG+Redis）
	if w.IsTTY {
		cfg.StorageMode = w.askChoice("2. 存储模式（lite=SQLite+本地, full=PG+Redis）",
			[]string{"full", "lite"}, "full")
	} else {
		// 非 TTY：保持默认 full（CI 场景通过 --config 文件注入）
		cfg.StorageMode = "full"
		logInfo("  ▶ 非 TTY：存储模式默认 full（如需 lite 请通过 --config 文件传入 STORAGE_MODE=lite）")
	}

	// 3. 主控端 URL（仅 full 模式有意义，lite 也写入以保持配置一致）
	cfg.MasterURL = w.askString("3. 主控端 URL (license 激活/心跳)", "https://llm.kxpms.cn")

	// 4. 应用端口
	cfg.AppPort = w.askInt("4. 应用端口 (HTTP)", 8781, []int{})

	// 5. PostgreSQL 端口
	cfg.PGPort = w.askInt("5. PostgreSQL 端口", 5432, []int{})

	// 6. Redis 端口
	cfg.RedisPort = w.askInt("6. Redis 端口", 6379, []int{})

	// 7. PostgreSQL 密码
	cfg.PGPassword = w.askPassword("7. PostgreSQL 密码")

	// 8. Redis 密码
	cfg.RedisPassword = w.askPassword("8. Redis 密码")

	// 9. API Key
	cfg.APIKey = w.askPassword("9. LLM Gateway API Key")

	// 10. Admin API Key
	cfg.AdminAPIKey = w.askPassword("10. LLM Gateway Admin API Key")

	// 11. JWT Secret
	cfg.JWTSecret = w.askPassword("11. JWT Secret")

	// 12. Credential Encryption Key
	cfg.CredEncryptKey = w.askPassword("12. 凭据加密 Key (32 字节 hex)")

	// 13. 镜像源策略
	cfg.ImageSourceStrategy = w.askChoice("13. 镜像源策略",
		[]string{"auto", "offline-only", "registry-only"},
		"auto")

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  配置确认")
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println(cfg.Summary())
	fmt.Println()

	if !w.askConfirm("确认开始安装? (Y/n): ", true) {
		return nil, fmt.Errorf("用户取消")
	}

	return cfg, nil
}

// logInfo 非 TTY 时在 wizard 内部给出提示
func logInfo(msg string) { fmt.Println(msg) }

// askString 询问字符串（有默认值）
func (w *Wizard) askString(prompt, defaultVal string) string {
	fmt.Printf("  %s [%s]: ", prompt, defaultVal)
	line, _ := w.reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

// askInt 询问整数
func (w *Wizard) askInt(prompt string, defaultVal int, exclude []int) int {
	for {
		fmt.Printf("  %s [%d]: ", prompt, defaultVal)
		line, _ := w.reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultVal
		}
		v, err := strconv.Atoi(line)
		if err != nil {
			fmt.Printf("    ⚠️  请输入整数\n")
			continue
		}
		if v < 1 || v > 65535 {
			fmt.Printf("    ⚠️  端口范围 1-65535\n")
			continue
		}
		// 检查冲突
		for _, e := range exclude {
			if v == e {
				fmt.Printf("    ⚠️  与其他端口冲突\n")
				goto retry
			}
		}
		return v
	retry:
	}
}

// askPassword 询问密码（TTY 时隐藏）
func (w *Wizard) askPassword(prompt string) string {
	fmt.Printf("  %s (留空自动生成): ", prompt)

	if w.IsTTY {
		pwd, err := term.ReadPassword(int(os.Stdin.Fd()))
		if err != nil {
			fmt.Println()
			fmt.Printf("    ⚠️  读取失败: %v\n", err)
			return w.genRandom()
		}
		fmt.Println()
		val := strings.TrimSpace(string(pwd))
		if val == "" {
			return w.genRandom()
		}
		return val
	}

	// 非 TTY：直接读
	line, _ := w.reader.ReadString('\n')
	val := strings.TrimSpace(line)
	if val == "" {
		return w.genRandom()
	}
	return val
}

// askChoice 询问选择
func (w *Wizard) askChoice(prompt string, options []string, defaultVal string) string {
	fmt.Printf("  %s\n", prompt)
	for i, o := range options {
		marker := " "
		if o == defaultVal {
			marker = "*"
		}
		fmt.Printf("    %s [%d] %s\n", marker, i+1, o)
	}
	fmt.Printf("  选择 [%s]: ", defaultVal)
	line, _ := w.reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	idx, err := strconv.Atoi(line)
	if err != nil || idx < 1 || idx > len(options) {
		return defaultVal
	}
	return options[idx-1]
}

// askConfirm 询问确认
func (w *Wizard) askConfirm(prompt string, defaultYes bool) bool {
	fmt.Printf("  %s", prompt)
	line, _ := w.reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return defaultYes
	}
	return line == "y" || line == "yes"
}

// genRandom 生成 32 字节 hex 随机串
// crypto/rand 失败属于系统级灾难，直接 panic 终止（避免写入弱密码到 .env）
func (w *Wizard) genRandom() string {
	v, err := secrets.GenerateRandom()
	if err != nil {
		panic(fmt.Errorf("生成随机数失败（系统 CSPRNG 不可用）: %w", err))
	}
	return v
}
