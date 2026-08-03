package caveman

import (
	"regexp"
)

// detectLanguage 检测文本语言，返回 8 语言之一。
// 对齐 languageDetector.ts:19-45。算法：CJK 脚本预过滤 + Latin 关键词打分。
//
// 返回值：zh（脚本路径）、en（默认）、pt-BR/es/de/fr/ja/id（关键词路径）。
func detectLanguage(text string) string {
	// CJK 预过滤：含汉字(U+4E00-9FFF) 且不含 kana(U+3040-30FF) → zh。
	if hanRE.MatchString(text) && !kanaRE.MatchString(text) {
		return "zh"
	}

	best := "en"
	bestScore := 0
	for _, lang := range languageHintOrder {
		pat := languageHints[lang]
		score := 0
		// 计总匹配数（对齐 TS：global + match.length 累加）。
		for _, m := range pat.FindAllString(text, -1) {
			score += len(m)
		}
		if score > bestScore {
			bestScore = score
			best = lang
		}
	}
	return best
}

// listSupportedCompressionLanguages 对齐 languageDetector.ts:47-51。
func listSupportedCompressionLanguages() []string {
	return append([]string{"en", "zh"}, languageHintOrder...)
}

// hanRE 匹配汉字 U+4E00-U+9FFF。对齐 languageDetector.ts:24。
var hanRE = regexp.MustCompile(`[一-鿿]`)

// kanaRE 匹配假名 U+3040-U+30FF（含平假名+片假名）。对齐 languageDetector.ts:24。
var kanaRE = regexp.MustCompile(`[぀-ヿ]`)

// languageHints 是各语言的关键词正则（case-insensitive，词边界）。
// 对齐 languageDetector.ts:1-11 LANGUAGE_HINTS。每语言一个 alternation。
// 只含 native-distinctive 词（排除 error/configuration 等跨语言通用词）。
var languageHints = map[string]*regexp.Regexp{
	"pt-BR": regexp.MustCompile(`(?i)\b(erro|função|configuração|banco de dados|por favor|obrigado|resposta|requisição|implementação)\b`),
	"es":    regexp.MustCompile(`(?i)\b(error|función|configuración|base de datos|por favor|gracias|respuesta|solicitud|implementación)\b`),
	"de":    regexp.MustCompile(`(?i)\b(Fehler|Funktion|Konfiguration|Datenbank|bitte|danke|Antwort|Anforderung|Implementierung)\b`),
	"fr":    regexp.MustCompile(`(?i)\b(erreur|fonction|configuration|base de données|merci|réponse|demande|implémentation)\b`),
	"ja":    regexp.MustCompile(`[ぁ-ヿ]`), // kana（汉字走 zh 路径）
	"id":    regexp.MustCompile(`(?i)\b(galat|fungsi|konfigurasi|basis data|terima kasih|respons|permintaan|implementasi)\b`),
}

// languageHintOrder 保证打分遍历顺序确定（map 遍历无序）。
// 对齐 TS 对象插入顺序：pt-BR, es, de, fr, ja, id。
var languageHintOrder = []string{"pt-BR", "es", "de", "fr", "ja", "id"}
