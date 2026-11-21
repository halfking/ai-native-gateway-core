// Package taskaggregator - TagNormalizer 实现标签归一化
package taskaggregator

import (
	"sort"
	"strings"
	"unicode"
)

// TagNormalizer 标签归一化器
type TagNormalizer struct {
	synonymMap map[string]string // 同义词映射
	stopWords  map[string]bool   // 停用词
}

// NewTagNormalizer 创建标签归一化器
func NewTagNormalizer() *TagNormalizer {
	return &TagNormalizer{
		synonymMap: buildSynonymMap(),
		stopWords:  buildStopWords(),
	}
}

// Normalize 归一化标签列表
func (n *TagNormalizer) Normalize(tags []string) []string {
	// 1. 预处理：转小写、去空白
	processed := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		tag = strings.ToLower(tag)
		if tag != "" {
			processed = append(processed, tag)
		}
	}

	// 2. 去停用词
	filtered := []string{}
	for _, tag := range processed {
		if !n.isStopWord(tag) {
			filtered = append(filtered, tag)
		}
	}

	// 3. 同义词合并
	normalized := []string{}
	for _, tag := range filtered {
		canonical := n.getCanonicalForm(tag)
		normalized = append(normalized, canonical)
	}

	// 4. 去重
	uniqueTags := deduplicatePreservingOrder(normalized)

	// 5. 排序（按字母序）
	sort.Strings(uniqueTags)

	// 6. 限制数量（最多10个）
	if len(uniqueTags) > 10 {
		uniqueTags = uniqueTags[:10]
	}

	return uniqueTags
}

// isStopWord 判断是否为停用词
func (n *TagNormalizer) isStopWord(word string) bool {
	return n.stopWords[word]
}

// getCanonicalForm 获取规范形式
func (n *TagNormalizer) getCanonicalForm(tag string) string {
	// 1. 去除非字母数字字符（保留中文、连字符和下划线）
	cleaned := cleanTag(tag)

	// 2. 查找同义词映射
	if canonical, ok := n.synonymMap[cleaned]; ok {
		return canonical
	}

	return cleaned
}

// cleanTag 清理标签
func cleanTag(tag string) string {
	var result strings.Builder
	for _, r := range tag {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			result.WriteRune(r)
		} else if unicode.Is(unicode.Han, r) {
			// 保留中文字符
			result.WriteRune(r)
		}
	}
	return result.String()
}

// deduplicatePreservingOrder 去重（保持顺序）
func deduplicatePreservingOrder(strs []string) []string {
	seen := make(map[string]bool)
	result := []string{}

	for _, s := range strs {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}

	return result
}

// buildSynonymMap 构建同义词映射
func buildSynonymMap() map[string]string {
	return map[string]string{
		// 编程语言
		"golang":     "go",
		"py":         "python",
		"js":         "javascript",
		"ts":         "typescript",
		"cpp":        "c++",
		"csharp":     "c#",
		
		// 框架和工具
		"react-js":   "react",
		"vue-js":     "vue",
		"postgresql": "postgres",
		"pg":         "postgres",
		"psql":       "postgres",
		"k8s":        "kubernetes",
		
		// 任务类型
		"bug-fix":    "bugfix",
		"bug":        "bugfix",
		"feat":       "feature",
		"refactor":   "refactoring",
		
		// 常见词汇
		"db":         "database",
		"api":        "api",
		"frontend":   "frontend",
		"backend":    "backend",
		"fullstack":  "full-stack",
		
		// 中文同义词
		"调试":       "debugging",
		"优化":       "optimization",
		"重构":       "refactoring",
		"测试":       "testing",
		"部署":       "deployment",
		"修复":       "bugfix",
		"新功能":     "feature",
	}
}

// buildStopWords 构建停用词列表
func buildStopWords() map[string]bool {
	words := []string{
		// 英文停用词
		"the", "a", "an", "and", "or", "but", "in", "on", "at", "to", "for",
		"of", "with", "by", "from", "up", "about", "into", "through", "during",
		"is", "are", "was", "were", "be", "been", "being", "have", "has", "had",
		"do", "does", "did", "will", "would", "could", "should", "may", "might",
		"this", "that", "these", "those", "i", "you", "he", "she", "it", "we", "they",
		
		// 中文停用词
		"的", "了", "和", "是", "在", "有", "个", "着", "这", "那", "就", "都", "而",
		"及", "与", "等", "对", "中", "也", "要", "不", "还", "可以", "进行", "一个",
	}

	stopWords := make(map[string]bool)
	for _, w := range words {
		stopWords[w] = true
	}

	return stopWords
}

// AddSynonym 添加自定义同义词
func (n *TagNormalizer) AddSynonym(from, to string) {
	n.synonymMap[strings.ToLower(from)] = strings.ToLower(to)
}

// AddStopWord 添加自定义停用词
func (n *TagNormalizer) AddStopWord(word string) {
	n.stopWords[strings.ToLower(word)] = true
}
