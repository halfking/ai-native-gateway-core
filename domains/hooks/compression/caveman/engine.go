package caveman

import (
	"regexp"
	"strings"
)

// applyRulesToText 对一段文本顺序应用规则，返回压缩后文本 + 应用的规则名。
// 对齐 caveman.ts:175-203。
func applyRulesToText(text string, rules []Rule) (string, []string) {
	result := text
	lower := strings.ToLower(text)
	applied := []string{}
	for _, rule := range rules {
		if !shouldAttemptRule(rule.Name, lower) {
			continue
		}
		before := result
		result = rule.Pattern.ReplaceAllStringFunc(result, rule.Replace)
		if result != before {
			applied = append(applied, rule.Name)
		}
	}
	return result, applied
}

// articleHintRE 用于 articles 规则的预过滤。对齐 caveman.ts:163。
var articleHintRE = regexp.MustCompile(`\b(?:a|an|the)\b`)

// shouldAttemptRule 做关键词预过滤，避免对每条规则都跑正则。
// 对齐 caveman.ts:165-173。articles 用专用 hint；其余用 RULE_KEYWORDS。
func shouldAttemptRule(ruleName, lowerText string) bool {
	if ruleName == "articles" {
		return articleHintRE.MatchString(lowerText)
	}
	keywords, ok := ruleKeywords[ruleName]
	if !ok || len(keywords) == 0 {
		return true // 无关键词表 → 总是尝试（保守）
	}
	for _, kw := range keywords {
		if strings.Contains(lowerText, kw) {
			return true
		}
	}
	return false
}

// ruleKeywords 是各规则的关键词预过滤表。对齐 caveman.ts:27-161 RULE_KEYWORDS。
// 只覆盖 en 规则名（非 en 规则名带语言前缀，无关键词表 → 总是尝试，保守正确）。
var ruleKeywords = map[string][]string{
	"redundant_phrasing":                {"make sure", "be sure"},
	"redundant_because":                 {"due to the fact", "the reason is because"},
	"redundant_directive":               {"it is important", "you should", "remember to"},
	"pleasantries":                      {"sure", "certainly", "of course", "happy to", "thanks", "thank you", "glad to help", "glad to", "no problem", "you're welcome", "youre welcome", "absolutely"},
	"polite_framing":                    {"please", "kindly", "could you please", "would you please", "can you please", "i would like you", "i want you", "i need you"},
	"hedging":                           {"it seems like", "it appears that", "i think that", "i believe that", "probably", "possibly", "maybe it"},
	"verbose_instructions":              {"provide a detailed", "give me a comprehensive", "write an in-depth", "create a thorough", "explain in detail"},
	"filler_adverbs":                    {"basically", "essentially", "actually", "literally", "simply", "currently"},
	"filler_phrases":                    {"i want to", "i need to", "i'd like to", "i'm looking for"},
	"redundant_openers":                 {"hi there", "hello", "good morning", "hey"},
	"verbose_requests":                  {"i was wondering", "would it be possible"},
	"leader_phrases":                    {"i'll", "i will", "i can", "i'd", "let me", "you can", "we will", "we can", "let's"},
	"self_reference":                    {"i am trying to", "i am working on", "i have been"},
	"excessive_gratitude":               {"thank you so much", "thanks in advance", "i really appreciate"},
	"qualifier_removal":                 {"a bit", "a little", "somewhat", "kind of", "sort of"},
	"softeners":                         {"if possible", "when you get a chance", "at your convenience", "just wondering"},
	"uncertainty_fillers":               {"i guess", "i suppose", "more or less", "in a way"},
	"assistant_fillers":                 {"here's", "below is", "this is"},
	"compound_collapse":                 {"and any potential"},
	"explanatory_prefix":                {"the function appears to be handling", "the code seems to", "the class is", "this module is"},
	"question_to_directive":             {"can you explain why", "could you show me how", "would you tell me", "can you tell me"},
	"context_setup":                     {"i have the following code", "here is my code", "below is the code"},
	"intent_clarification":              {"what i'm trying to do", "my objective is to", "what i need is", "i'm aiming to"},
	"background_removal":                {"as you may know", "as we discussed earlier"},
	"meta_commentary":                   {"note that", "keep in mind", "remember that"},
	"purpose_statement":                 {"for the purpose of", "with the goal of", "in an effort to", "for every"},
	"list_conjunction":                  {"and also", "as well as"},
	"purpose_phrases":                   {"in order to", "so as to"},
	"redundant_quantifiers":             {"each and every", "any and all"},
	"all_quantifier":                    {"any and all"},
	"verbose_connectors":                {"furthermore", "additionally", "moreover", "in addition"},
	"transition_removal":                {"on the other hand", "in contrast", "however"},
	"emphasis_removal":                  {"very", "really", "extremely", "highly", "quite"},
	"passive_voice":                     {"is being used", "is being called", "is being generated", "was created", "was generated", "was implemented"},
	"repeated_context":                  {"as we discussed earlier", "as mentioned before", "as previously stated", "as i said before"},
	"repeated_question":                 {"same question as before", "i asked this earlier", "this is the same question"},
	"reestablished_context":             {"going back to the code above", "referring back to", "returning to"},
	"summary_replacement":               {"to summarize", "in summary of our conversation", "to recap"},
	"ultra_abbreviations":               {"database"},
	"ultra_config_abbreviation":         {"configuration"},
	"ultra_function_abbreviation":       {"function"},
	"ultra_request_abbreviation":        {"request"},
	"ultra_response_abbreviation":       {"response"},
	"ultra_implementation_abbreviation": {"implementation"},
	"ultra_authentication_abbreviation": {"authentication"},
	"ultra_authorization_abbreviation":  {"authorization"},
	"ultra_application_abbreviation":    {"application"},
	"ultra_dependency_abbreviation":     {"dependency", "dependencies"},
	"ultra_common_abbreviations":        {"implementation", "authentication", "authorization", "application", "dependency", "dependencies"},
}

// ---- cleanupArtifacts (caveman.ts:205-219) ----
// 规则应用后清理产生的空白/标点 artifacts。全字符扫描。

func cleanupArtifacts(text string) string {
	result := text
	if hasRepeatedHorizontalWhitespace(result) {
		result = collapseHorizontalWhitespaceRuns(result)
	}
	result = removeHorizontalWhitespaceBeforePunctuation(result)
	result = collapseRepeatedSentencePunctuation(result)
	if strings.Contains(result, " \n") || strings.Contains(result, "\t\n") {
		result = stripLineTrailingHorizontalWhitespace(result)
	}
	if strings.HasSuffix(result, " ") || strings.HasSuffix(result, "\t") {
		result = strings.TrimRight(result, " \t")
	}
	if strings.Contains(result, "\n\n\n") {
		result = collapseExcessNewlines(result)
	}
	result = strings.TrimLeft(result, "\n")
	result = strings.TrimRight(result, "\n")
	return result
}

func isHorizontalWhitespace(r rune) bool { return r == ' ' || r == '\t' }

func isSentencePunctuation(r rune) bool { return r == '.' || r == '!' || r == '?' }

func isCleanupPunctuation(r rune) bool {
	return r == ',' || r == '.' || r == ';' || r == ':' || r == '!' || r == '?'
}

func hasRepeatedHorizontalWhitespace(text string) bool {
	prevWS := false
	for _, r := range text {
		curWS := isHorizontalWhitespace(r)
		if curWS && prevWS {
			return true
		}
		prevWS = curWS
	}
	return false
}

func collapseHorizontalWhitespaceRuns(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	runes := []rune(text)
	changed := false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if !isHorizontalWhitespace(r) {
			b.WriteRune(r)
			continue
		}
		start := i
		for i+1 < len(runes) && isHorizontalWhitespace(runes[i+1]) {
			i++
		}
		if i > start {
			b.WriteByte(' ')
			changed = true
		} else {
			b.WriteRune(r)
		}
	}
	if !changed {
		return text
	}
	return b.String()
}

func removeHorizontalWhitespaceBeforePunctuation(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	runes := []rune(text)
	changed := false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if !isHorizontalWhitespace(r) {
			b.WriteRune(r)
			continue
		}
		start := i
		for i+1 < len(runes) && isHorizontalWhitespace(runes[i+1]) {
			i++
		}
		if i+1 < len(runes) && isCleanupPunctuation(runes[i+1]) {
			changed = true
			continue
		}
		for j := start; j <= i; j++ {
			b.WriteRune(runes[j])
		}
	}
	if !changed {
		return text
	}
	return b.String()
}

func collapseRepeatedSentencePunctuation(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	runes := []rune(text)
	changed := false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if !isSentencePunctuation(r) {
			b.WriteRune(r)
			continue
		}
		last := r
		for i+1 < len(runes) && isSentencePunctuation(runes[i+1]) {
			i++
			last = runes[i]
		}
		if last != r {
			changed = true
		}
		b.WriteRune(last)
	}
	if !changed {
		return text
	}
	return b.String()
}

func stripLineTrailingHorizontalWhitespace(text string) string {
	lines := strings.Split(text, "\n")
	changed := false
	for i, ln := range lines {
		cleaned := strings.TrimRight(ln, " \t")
		if cleaned != ln {
			changed = true
			lines[i] = cleaned
		}
	}
	if !changed {
		return text
	}
	return strings.Join(lines, "\n")
}

func collapseExcessNewlines(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	changed := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '\n' {
			b.WriteByte(c)
			continue
		}
		start := i
		for i+1 < len(text) && text[i+1] == '\n' {
			i++
		}
		count := i - start + 1
		if count > 2 {
			b.WriteString("\n\n")
			changed = true
		} else {
			b.WriteString(text[start : i+1])
		}
	}
	if !changed {
		return text
	}
	return b.String()
}

// ---- recapitalizeSentences (caveman.ts:389-393) ----
// 规则可能把句首小写化，这里把每句首字母重新大写。

var recapitalizeRE = regexp.MustCompile(`(^|[.!?][ \t]|\n[ \t]*)([a-z])`)

func recapitalizeSentences(text string) string {
	return recapitalizeRE.ReplaceAllStringFunc(text, func(m string) string {
		sub := recapitalizeRE.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		// sub[1]=前缀，sub[2]=首字母。大写首字母。
		return sub[1] + strings.ToUpper(sub[2])
	})
}
