package caveman

import (
	"embed"
	"path"
	"sort"
)

//go:embed data
var ruleFS embed.FS

// 注：embed 模式 "data" 递归嵌入整个 data 目录树（含 8 语言子目录）。

// languageFiles 返回某语言目录下所有 .json 的 {filename: bytes}。
// filename 是不含路径的 basename（如 "filler.json"），用于 loadAllRulesForLanguage。
// 对齐 ruleLoader.ts:readPack 的路径模型，但用 embed.FS 替代 node:fs。
func languageFiles(language string) map[string][]byte {
	dir := path.Join("data", language)
	entries, err := ruleFS.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := ruleFS.ReadFile(path.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out[e.Name()] = raw
	}
	return out
}

// availableLanguages 返回 data/ 下所有语言目录名（排序）。对齐
// ruleLoader.ts:getAvailableLanguagePacks。
func availableLanguages() []string {
	entries, err := ruleFS.ReadDir("data")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// LoadAllRulesForLanguage 是公开的规则加载入口（供测试和未来外部使用）。
// 结果缓存。未知语言返回 nil。
func LoadAllRulesForLanguage(language string) []Rule {
	return loadAllRulesForLanguage(language, languageFiles(language))
}

// resolveLanguage 按 caveman.ts:521-535 的逻辑解析最终使用的语言规则包。
// autoDetect=true 时直接用 detected；否则看 enabledPacks 是否含 detected，
// 不含则回退 en，再不行用 detected。对齐 caveman.ts:529-535。
func resolveLanguage(detected string, autoDetect bool, enabledPacks []string) string {
	if autoDetect {
		return detected
	}
	for _, p := range enabledPacks {
		if p == detected {
			return detected
		}
	}
	for _, p := range enabledPacks {
		if p == "en" {
			return "en"
		}
	}
	return detected
}
