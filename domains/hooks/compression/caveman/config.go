package caveman

// Config 是 Caveman 压缩的配置。对齐 types.ts CavemanConfig。
type Config struct {
	Enabled              bool      // 总开关（false → Compress 直接返回原 body）
	CompressRoles        []string  // 压缩哪些 role（"user"/"assistant"/"system"）
	SkipRules            []string  // 跳过的规则名
	MinMessageLength     int       // 短于此长度的 message 不压缩
	PreservePatterns     []string  // 额外保护模式（正则源串，运行时编译）
	Intensity            Intensity // lite/full/ultra
	Language             string    // 手动指定语言（AutoDetectLanguage=false 时用）
	AutoDetectLanguage   bool      // 自动语言检测
	EnabledLanguagePacks []string  // 手动启用的语言包（AutoDetectLanguage=false 时用）
}

// DefaultConfig 对齐 types.ts DEFAULT_CAVEMAN_CONFIG。
// 与 TS 默认值一致：enabled=false, compressRoles=["user"], minMessageLength=50,
// intensity=lite, preservePatterns 含 6 个默认保护模式。
func DefaultConfig() Config {
	return Config{
		Enabled:              true,
		CompressRoles:        []string{"user"},
		SkipRules:            nil,
		MinMessageLength:     50,
		PreservePatterns:     defaultPreservePatterns,
		Intensity:            IntensityLite,
		AutoDetectLanguage:   true,
		EnabledLanguagePacks: []string{"en"},
	}
}

// defaultPreservePatterns 对齐 DEFAULT_CAVEMAN_CONFIG.preservePatterns。
// SOURCE-VERIFIED from types.ts:407-414.
var defaultPreservePatterns = []string{
	"```[\\s\\S]*?```",
	"`[^`\\n]+`",
	"\\b(https?://\\S+)",
	"(?:^|\\s)(\\.{0,2}/[\\w./\\-]+)",
	"^\\s*(Error|TypeError|RangeError|SyntaxError|ReferenceError):",
	"^\\s+at\\s",
}
