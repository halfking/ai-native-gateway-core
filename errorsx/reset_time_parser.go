// Package errorsx — reset_time_parser.go
//
// P0-4 (docs/research/2026-10-14-magpie对比与优化方案.md):
//
//  1. ParseQuotaResetAt — 从 429/402/5xx-quota 错误正文里提取上游声明的
//     「配额重置时刻」（绝对时刻 + 相对时长两族，中英文 + \uXXXX 转义体），
//     供凭据熔断把 quota 冷却窗对齐到真实重置点（rest 分级），而不是粗粒度
//     固定指数窗。
//  2. GradeQuotaRest / IsCreditExhausted — 402「欠费」(credit) 与「配额窗」
//     (quota) 二分：money 词命中 → credit 类（固定 30min 量级冷却，无重置
//     语义，只能等运维充值）；quota 词 → quota 类（对齐 resetsAt）。
//     不新增 ErrorKind 字符串值，DB 旧值保持可读。
//  3. ModelScopedRejection — 上游错误正文**指名某模型**（not found /
//     deprecated / retired / EOL / unsupported）时提取模型名，供调用方把
//     失败隔离到 (credential, model) 绑定级，而不是连坐整凭据。
//
// 判定入口均为无状态纯函数；是否处于 quota 语境（429/402/5xx-quota）由
// 调用方判（与 ParseContextLimitFromError 的分工模式一致）。
package errorsx

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// ---------------------------------------------------------------------------
// \uXXXX 解码
// ---------------------------------------------------------------------------

// unicodeEscapeRe matches JSON-style \uXXXX escapes. Domestic providers that
// return ASCII-escaped JSON bodies ("\u60a8\u7684...") would otherwise never
// match the CJK patterns below.
var unicodeEscapeRe = regexp.MustCompile(`\\u([0-9a-fA-F]{4})`)

// decodeResetUnicodeEscapes replaces \uXXXX escapes (including UTF-16
// surrogate pairs) with the runes they denote. Non-escape bytes pass through
// verbatim; malformed escapes are kept as-is. Bodies without \u return
// unchanged.
//
// Name note: the parallel P0-x session's unsupported_param_field.go keeps its
// own []byte decoder (decodeRefusalUnicodeEscapes) — same package, same idea,
// deliberately not shared to avoid coupling two in-flight files through a
// private helper.
func decodeResetUnicodeEscapes(s string) string {
	if !strings.Contains(s, `\u`) {
		return s
	}
	locs := unicodeEscapeRe.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for i := 0; i < len(locs); i++ {
		loc := locs[i]
		if loc[0] < last {
			// Already consumed as the low half of a surrogate pair.
			continue
		}
		b.WriteString(s[last:loc[0]])
		code, err := strconv.ParseUint(s[loc[0]+2:loc[1]], 16, 32)
		if err != nil {
			b.WriteString(s[loc[0]:loc[1]])
			last = loc[1]
			continue
		}
		r1 := rune(code)
		if utf16.IsSurrogate(r1) {
			// Surrogate pair: consume the immediately-following \uXXXX if it
			// is the matching low surrogate. The pair is one rune; skip the
			// next loc by advancing `last` past it (its WriteString segment
			// becomes empty).
			if loc[1]+6 <= len(s) && s[loc[1]:loc[1]+2] == `\u` {
				if code2, err2 := strconv.ParseUint(s[loc[1]+2:loc[1]+6], 16, 32); err2 == nil {
					if dec := utf16.DecodeRune(r1, rune(code2)); dec != 0xFFFD {
						b.WriteRune(dec)
						last = loc[1] + 6
						continue
					}
				}
			}
			// Unpaired surrogate: keep raw.
			b.WriteString(s[loc[0]:loc[1]])
			last = loc[1]
			continue
		}
		b.WriteRune(r1)
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// ---------------------------------------------------------------------------
// 相对时长（相对 now 计算）
// ---------------------------------------------------------------------------

// quotaRelativeResetRe anchors an English relative-reset phrase and captures
// the duration window that follows:
//
//	"resets in 0h 19min"   → anchor "resets in",  window "0h 19min"
//	"will reset after 5 hours"              → "reset after", "5 hours"
//	"try again in 1m30s"                    → "try again in", "1m30s"
//	"quota will become available in 30 seconds"
//
// The window is then scanned word-by-word by quotaDurationWordRe. Anchoring
// on the reset-family verb is deliberate: a bare "in 5 hours" in unrelated
// prose would otherwise be mistaken for a reset hint.
var quotaRelativeResetRe = regexp.MustCompile(
	`(?i)(?:resets?|recovers?|resumes?|retry|retries|try\s+again|available|restored?)\s+(?:in|after)\s*[:：,，]?\s*([0-9][0-9a-zA-Z.,\s]{0,48})`)

// quotaRelativeResetCJKRe is the Chinese order (duration BEFORE the verb):
// 「将在 5 小时后重置」「预计 30 分钟后恢复」「2 小时后可用」.
var quotaRelativeResetCJKRe = regexp.MustCompile(
	`((?:[0-9]+(?:\.[0-9]+)?\s*(?:毫秒|小时|分钟|分|秒|天)[\s,，]*)+)后\s*[,，。]?\s*(?:重置|恢复|可用|重试|开放|解除)`)

// quotaBareAfterRe is the last-resort fallback for a bare "after 5 hours" with
// NO reset verb in front (the P0-4 doc lists this phrasing). Tried ONLY after
// the anchored patterns fail so unrelated prose ("completed in 2 hours") in a
// quota body cannot win over an anchored/absolute hint.
var quotaBareAfterRe = regexp.MustCompile(`(?i)\bafter\s*[:：,，]?\s*([0-9][0-9a-zA-Z.,\s]{0,48})`)

// quotaDurationWordRe scans one number+unit word inside a captured duration
// window. Unit alternation order matters: "ms" must precede both "m" and "s"
// ("500ms" must not read as 500 minutes), and 分钟 must precede 分.
var quotaDurationWordRe = regexp.MustCompile(
	`([0-9]+(?:\.[0-9]+)?)\s*(ms|毫秒|secs?|seconds?|sec|s|秒|mins?|minutes?|min|m|分(?:钟)?|hrs?|hours?|hr|h|小时|days?|d|天)`)

// parseDurationWords sums the number+unit words found in a duration window.
// Returns (0, false) when no unit word matched (e.g. the window was a date).
func parseDurationWords(window string) (time.Duration, bool) {
	matches := quotaDurationWordRe.FindAllStringSubmatch(window, -1)
	if len(matches) == 0 {
		return 0, false
	}
	var total time.Duration
	for _, m := range matches {
		val, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		var unit time.Duration
		switch strings.ToLower(m[2]) {
		case "ms", "毫秒":
			unit = time.Millisecond
		case "s", "sec", "secs", "second", "seconds", "秒":
			unit = time.Second
		case "m", "min", "mins", "minute", "minutes", "分", "分钟":
			unit = time.Minute
		case "h", "hr", "hrs", "hour", "hours", "小时":
			unit = time.Hour
		case "d", "day", "days", "天":
			unit = 24 * time.Hour
		default:
			continue
		}
		total += time.Duration(val * float64(unit))
	}
	return total, total > 0
}

// ---------------------------------------------------------------------------
// 绝对时刻
// ---------------------------------------------------------------------------

// quotaResetTimestampRe matches an absolute reset timestamp with tolerant
// zone suffixes:
//
//	"2026-10-11T20:16:02Z"                 → UTC
//	"2026-10-11 20:16:02 UTC+8重置"        → +8h offset (task doc's exact shape)
//	"2026-10-11 20:16:02 +08:00"           → numeric offset
//	"2026-10-11 20:16:02"                  → naive, treated as UTC
//	"2026/10/11 20:16:02"                  → slash form
//
// Naive-without-zone is deliberately parsed as UTC — same contract as
// scanQuotaResetTimestamp / NextQuotaReset (classify.go), so the two scanners
// cannot disagree on the same body.
var quotaResetTimestampRe = regexp.MustCompile(
	`(?P<year>\d{4})[-/](?P<month>\d{2})[-/](?P<day>\d{2})[T ]` +
		`(?P<hour>\d{1,2}):(?P<minute>\d{2})(?::(?P<second>\d{2}))?(?:\.(?P<nanos>\d{1,9}))?` +
		`(?:\s*(?P<znamed>UTC|GMT)(?:\s*(?P<zsign>[+-])(?P<zhour>\d{1,2})(?::?(?P<zminute>\d{2}))?)?` +
		`|(?P<zzulu>Z)` +
		`|(?P<zsign2>[+-])(?P<zhour2>\d{1,2})(?::?(?P<zminute2>\d{2}))?)?`)

// scanQuotaResetTimestampAt scans decoded for absolute reset timestamps and
// returns the first candidate that is not in the past (1-minute grace, same
// as the legacy scanner). Past-only bodies return false: a reset that already
// elapsed is not a usable cooling target.
func scanQuotaResetTimestampAt(decoded string, now time.Time) (time.Time, bool) {
	reNames := quotaResetTimestampRe.SubexpNames()
	for _, m := range quotaResetTimestampRe.FindAllStringSubmatch(decoded, -1) {
		group := func(name string) string {
			idx := -1
			for i, n := range reNames {
				if n == name {
					idx = i
					break
				}
			}
			if idx < 0 || idx >= len(m) {
				return ""
			}
			return m[idx]
		}
		atoi := func(s string, def int) int {
			if s == "" {
				return def
			}
			v, err := strconv.Atoi(s)
			if err != nil {
				return def
			}
			return v
		}
		year := atoi(group("year"), 0)
		month := atoi(group("month"), 0)
		day := atoi(group("day"), 0)
		hour := atoi(group("hour"), 0)
		minute := atoi(group("minute"), 0)
		second := atoi(group("second"), 0)
		if year < 2000 || month < 1 || month > 12 || day < 1 || day > 31 ||
			hour > 23 || minute > 59 || second > 59 {
			continue
		}
		nanosStr := group("nanos")
		nanos := 0
		if nanosStr != "" {
			padded := (nanosStr + "000000000")[:9]
			if v, err := strconv.Atoi(padded); err == nil {
				nanos = v
			}
		}
		// Zone offset seconds east of UTC.
		offset := 0
		switch {
		case group("zzulu") == "Z":
			offset = 0
		default:
			sign := 1
			zhour := ""
			zminute := ""
			if group("znamed") != "" || group("zsign") != "" {
				if group("zsign") == "-" {
					sign = -1
				}
				zhour = group("zhour")
				zminute = group("zminute")
			} else if group("zsign2") != "" {
				if group("zsign2") == "-" {
					sign = -1
				}
				zhour = group("zhour2")
				zminute = group("zminute2")
			}
			if zhour != "" {
				offset = atoi(zhour, 0) * 3600
				if zminute != "" {
					offset += atoi(zminute, 0) * 60
				}
				offset *= sign
			}
		}
		t := time.Date(year, time.Month(month), day, hour, minute, second, nanos, time.UTC)
		t = t.Add(-time.Duration(offset) * time.Second)
		if t.Before(now.Add(-1 * time.Minute)) {
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

// ---------------------------------------------------------------------------
// ParseQuotaResetAt
// ---------------------------------------------------------------------------

// maxQuotaResetLead bounds the accepted distance between now and a parsed
// reset moment (absolute or relative). Monthly windows can legitimately be
// ~31 days out; anything beyond 40 days is garbage, not a reset hint.
const maxQuotaResetLead = 40 * 24 * time.Hour

// ParseQuotaResetAt extracts the upstream-declared quota reset moment from a
// 429/402/5xx-quota error body. The caller is responsible for gating on the
// quota context (status / ErrorKind); the parser itself is stateless.
//
// Order of attempts:
//  1. absolute timestamp (zone-suffix tolerant, see quotaResetTimestampRe);
//  2. anchored relative duration ("resets in 0h 19min", 「5 小时后重置」);
//  3. bare "after <duration>" fallback (last, see quotaBareAfterRe).
//
// Returns ok=false when nothing parses, when every timestamp candidate is in
// the past, or when the parsed lead exceeds maxQuotaResetLead.
func ParseQuotaResetAt(body []byte, now time.Time) (time.Time, bool) {
	if len(body) == 0 {
		return time.Time{}, false
	}
	decoded := decodeResetUnicodeEscapes(string(body))

	// 1. Absolute timestamps are the most precise evidence — try first.
	if t, ok := scanQuotaResetTimestampAt(decoded, now); ok {
		if lead := t.Sub(now); lead > 0 && lead <= maxQuotaResetLead {
			return t, true
		}
	}

	// 2. Anchored relative durations.
	if m := quotaRelativeResetRe.FindStringSubmatch(decoded); m != nil {
		if d, ok := parseDurationWords(m[1]); ok && d > 0 && d <= maxQuotaResetLead {
			return now.Add(d), true
		}
	}
	if m := quotaRelativeResetCJKRe.FindStringSubmatch(decoded); m != nil {
		if d, ok := parseDurationWords(m[1]); ok && d > 0 && d <= maxQuotaResetLead {
			return now.Add(d), true
		}
	}

	// 3. Bare "after <duration>" fallback.
	if m := quotaBareAfterRe.FindStringSubmatch(decoded); m != nil {
		if d, ok := parseDurationWords(m[1]); ok && d > 0 && d <= maxQuotaResetLead {
			return now.Add(d), true
		}
	}
	return time.Time{}, false
}

// ---------------------------------------------------------------------------
// 402 credit / quota 二分
// ---------------------------------------------------------------------------

// CreditExhaustedCooling is the rest-grade cooling for money-class (credit)
// exhaustion: no reset semantics exist — the account recovers only when an
// operator tops up — so a flat ~30-minute re-probe cadence replaces both the
// permanent quarantine and the reset alignment.
const CreditExhaustedCooling = 30 * time.Minute

// creditExhaustionRe — money words (P0-4): billing-plane exhaustion that only
// a top-up can fix. Kept narrower than budgetExceededRe on purpose: "quota"/
// "usage limit"/"配额" are window semantics (they roll), while credit/balance/
// billing/arrears/欠费/余额/充值/费用 name the money plane.
var creditExhaustionRe = regexp.MustCompile(
	`(?i)(credits?\b|balance|billing|arrears|top[ _-]?up|recharge|` +
		`"code"\s*:\s*"(?:INSUFFICIENT_BALANCE|insufficient_balance|insufficient_credit|INSUFFICIENT_G1_CREDITS_BALANCE)"|` +
		`欠费|余额|充值|费用|账单)`)

// IsQuotaExhaustion reports whether kind belongs to the quota-exhaustion
// family (the four quota ErrorKinds; rate_limit is deliberately excluded —
// it is a concurrency/capacity signal, not a quota window).
func IsQuotaExhaustion(kind ErrorKind) bool {
	switch kind {
	case KindQuota, KindQuotaPeriodic, KindQuotaBalance, KindQuotaPermanent:
		return true
	default:
		return false
	}
}

// IsCreditExhausted reports whether a quota-family failure is money-class
// (credit/balance/billing exhaustion) as opposed to a rolling quota window.
// Non-quota kinds always return false — the predicate grades an ALREADY
// quota-classified failure, it does not classify.
//
// Callers use this for the 402 split (P0-4): credit → flat
// CreditExhaustedCooling re-probe; quota window → align to ParseQuotaResetAt.
func IsCreditExhausted(kind ErrorKind, body []byte) bool {
	if !IsQuotaExhaustion(kind) || len(body) == 0 {
		return false
	}
	return creditExhaustionRe.MatchString(decodeResetUnicodeEscapes(string(body)))
}

// QuotaRestClass grades how a quota-exhausted credential should "rest"
// (cool) before the next attempt.
type QuotaRestClass string

const (
	// QuotaRestPeriodic: the body carries a usable reset signal — cool until
	// that moment (clamped by the breaker).
	QuotaRestPeriodic QuotaRestClass = "periodic"
	// QuotaRestCredit: money-plane exhaustion — flat CreditExhaustedCooling.
	QuotaRestCredit QuotaRestClass = "credit"
	// QuotaRestPermanent: no reset signal, no money word — keep the legacy
	// behaviour (permanent / exponential policy cooling).
	QuotaRestPermanent QuotaRestClass = "permanent"
)

// QuotaRestHint is the structured rest-grade for one quota failure body.
type QuotaRestHint struct {
	Class    QuotaRestClass
	ResetAt  time.Time // valid iff HasReset
	HasReset bool
	// Source records which pattern produced the verdict, for audit trails:
	// "absolute_timestamp" | "relative_duration" | "bare_after" |
	// "credit_word" | "" (permanent).
	Source string
}

// GradeQuotaRest grades a 429/402/5xx-quota body into the P0-4 rest classes.
// Precedence (documented, order is load-bearing):
//
//  1. absolute reset timestamp  → periodic (most precise evidence; even a
//     money-worded body like "balance insufficient, will reset at X" rolls
//     at X — Anthropic-style budget windows do exactly this);
//  2. money word                → credit (a boilerplate "try again later"
//     on an empty account must NOT under-cool a dead balance);
//  3. anchored relative duration→ periodic;
//  4. bare "after <duration>"   → periodic;
//  5. otherwise                 → permanent (legacy behaviour).
func GradeQuotaRest(body []byte, now time.Time) QuotaRestHint {
	if len(body) == 0 {
		return QuotaRestHint{Class: QuotaRestPermanent}
	}
	decoded := decodeResetUnicodeEscapes(string(body))

	if t, ok := scanQuotaResetTimestampAt(decoded, now); ok {
		if lead := t.Sub(now); lead > 0 && lead <= maxQuotaResetLead {
			return QuotaRestHint{Class: QuotaRestPeriodic, ResetAt: t, HasReset: true, Source: "absolute_timestamp"}
		}
	}
	if creditExhaustionRe.MatchString(decoded) {
		return QuotaRestHint{
			Class:    QuotaRestCredit,
			ResetAt:  now.Add(CreditExhaustedCooling),
			HasReset: true,
			Source:   "credit_word",
		}
	}
	// Step 1 already consumed the absolute case, so a hit here is a relative
	// hint; the re-scan only labels which of the two relative shapes fired.
	if t, ok := ParseQuotaResetAt(body, now); ok {
		src := "relative_duration"
		if !quotaRelativeResetRe.MatchString(decoded) && !quotaRelativeResetCJKRe.MatchString(decoded) {
			src = "bare_after"
		}
		return QuotaRestHint{Class: QuotaRestPeriodic, ResetAt: t, HasReset: true, Source: src}
	}
	return QuotaRestHint{Class: QuotaRestPermanent}
}

// ---------------------------------------------------------------------------
// 模型级隔离判定
// ---------------------------------------------------------------------------

// modelScopedStopWords are function words the identifier capture can grab on
// name-less bodies ("model is unknown" → capture "is"). A capture equal to
// one of these means the body names NO model, so no binding can be isolated.
var modelScopedStopWords = map[string]bool{
	"is": true, "are": true, "was": true, "were": true, "be": true, "been": true,
	"does": true, "do": true, "not": true, "no": true, "this": true, "that": true,
	"the": true, "your": true, "you": true, "we": true, "it": true, "its": true,
	"has": true, "have": true, "had": true, "will": true, "would": true,
	"cannot": true, "can": true, "must": true, "should": true, "may": true,
	"name": true, "with": true, "for": true, "unknown": true, "found": true,
}

// modelScopedRejectionRes matches upstream bodies whose failure subject is a
// NAMED model — the not-found / deprecated / retired / EOL / unsupported
// family (KindModelNotFound / KindModelDeprecated vocabulary, plus the NVIDIA
// NIM "Function '<id>': Not found" shape). The capture group isolates the
// model identifier so the failure can be scoped to one (credential, model)
// binding instead of the whole credential (P0-4 model-level isolation).
//
// Verdict alternation deliberately EXCLUDES bare "not available" (region/tier
// restrictions — see modelNotFoundRe's comment for the historical false
// positives) and excludes the OneAPI "No available channel" family (that is
// KindNoAvailableChannel, a transient routing gap, not a dead model).
var modelScopedRejectionRes = []*regexp.Regexp{
	// "The model 'gpt-5-fake' does not exist" / "model: claude-opus-4-6 not found"
	// / "model glm-5.1 not found" / "model glm-4 has been deprecated, please use glm-5"
	regexp.MustCompile(`(?i)\b(?:model|endpoint)[\s:]+['"\x60]?([a-z0-9._\-/:]{1,80})['"\x60]?\s*[:：]?\s+(?:does\s+not\s+exist|is\s+not\s+found|not\s+found|is\s+unknown|unknown|has\s+been\s+deprecated|is\s+deprecated|has\s+been\s+(?:retired|decommissioned|discontinued|sunset|permanently\s+removed)|is\s+no\s+longer\s+available|has\s+reached\s+(?:its\s+)?end[ _-]?of[ _-]?life|is\s+unsupported|unsupported)`),
	// NVIDIA NIM: "Function '<id>': Not found for account '<acct>'"
	regexp.MustCompile(`(?i)\bfunction\s+['"\x60]([a-z0-9._\-/:]{1,80})['"\x60]\s*[:：]\s*not\s+found`),
	// "no such model: foo" / "unknown model foo"
	regexp.MustCompile(`(?i)\b(?:no\s+such|unknown)\s+(?:model|function)\s*[:：]?\s*['"\x60]?([a-z0-9._\-/:]{1,80})`),
	// 「模型 xxx 不存在 / 未找到 / 已下线 / 已停用 / 不支持」
	regexp.MustCompile(`(?i)模型\s*['"\x60]?([a-z0-9._\-/:]{1,80})['"\x60]?\s*.{0,12}(?:不存在|未找到|已下线|已停用|已废弃|不支持)`),
}

// ModelScopedRejection reports whether the upstream error body explicitly
// names a model as the failure subject (not found / deprecated / retired /
// EOL / unsupported family) and, when it does, returns the named model.
//
// When ok is false the caller MUST keep whatever credential-level behaviour
// it already had — an error that does not name a model must not be silently
// downgraded to binding scope (P0-4 precondition: no false positives).
// Likewise a body that only carries `"code":"model_not_found"` without an
// identifier returns false: with no name there is no binding to isolate.
func ModelScopedRejection(body []byte) (string, bool) {
	if len(body) == 0 {
		return "", false
	}
	decoded := decodeResetUnicodeEscapes(string(body))
	for _, re := range modelScopedRejectionRes {
		m := re.FindStringSubmatch(decoded)
		if m == nil || len(m) < 2 {
			continue
		}
		name := strings.TrimSpace(m[1])
		if name == "" || len(name) < 2 {
			continue
		}
		if modelScopedStopWords[strings.ToLower(name)] {
			continue
		}
		return name, true
	}
	return "", false
}
