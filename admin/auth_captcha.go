// Package admin — 登录图形验证码（2026-10-10）。
//
// 本文件是登录验证码的唯一事实源：签发、PNG 位图渲染、一次性校验、连续失败计数。
//
// 设计要点（不要在没有读懂之前改）：
//
//  1. 服务端校验。答案**不发给客户端**，服务端只存 sha256 摘要。
//     纯前端校验的验证码挡不住撞库脚本，本文件的存在意义就是那层校验。
//  2. **必须渲染成 PNG 位图，不能用 SVG `<text>`**（2026-10-10 实测修正）。
//     SVG 版本把字符放进 `<text>…</text>`，于是
//     `curl …/api/auth/captcha | grep -o '<text[^>]*>[^<]*'` 就能零成本读出
//     答案——脚本根本不需要 OCR，验证码对自动化攻击完全透明。
//     现在用内建 5×7 点阵字体把字符**逐像素**画进 PNG（见 glyph5x7），
//     响应里没有任何一处机器可直读的答案。
//  3. 一次性 + 短 TTL。`verify` 成功或失败都会**立即删除**该条目，
//     所以一次验证码只能被提交一次；TTL 到期也一并清理。
//  4. 失败次数限流（CaptchaMaxAttempts）。不设的话，验证码本身就成了
//     一个无成本的口令爆破入口——按本文件字符表（30 个字符）算，
//     30^4 ≈ 81 万种可能，没有尝试上限就等于没有门槛。
//  5. 自适应强制：连续失败 CaptchaFailureThreshold 次后，`/api/auth/captcha`
//     的 `required` 变 true，此时不携带验证码直接登录一律 400。
//     未达阈值时验证码是「可选」的，正常用户不必被打扰。
//  6. 内存态。与 loginLimiter(rate_limit.go) 同款：网关重启即清空，
//     代价是重启可让攻击者重置失败计数——这是既有设计的一致取舍，
//     不在这里单独引入第二套持久化。
//
//  零新依赖：PNG 编码（image/png）与点阵字模都在本文件内，标准库足够。
package admin

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ── 参数 SSOT ──────────────────────────────────────────────────────────────

const (
	// CaptchaTTL 是验证码有效期。短到足以挡住「截图复用」，长到够一个人输完。
	CaptchaTTL = 5 * time.Minute

	// CaptchaLength 是答案字符数。4 位 + 去歧义字符集 ≈ 30^4 ≈ 81 万种可能，
	// 配合 CaptchaMaxAttempts 的尝试上限，无法枚举。
	CaptchaLength = 4

	// CaptchaMaxAttempts 是单个验证码允许的提交次数（含正确那次）。
	// 用尽即删除，必须重新申领。
	CaptchaMaxAttempts = 3

	// CaptchaMaxEntries 是在存储条目数上限。攻击者可以疯狂调签发接口，
	// 没有这个上限就是一条 OOM 路径。到达上限时清掉已过期项，
	// 仍超则拒发（503）——宁可让验证码短暂不可用，也不无界增长。
	CaptchaMaxEntries = 4096

	// CaptchaFailureThreshold 是**强制**验证码所需的连续失败次数。
	// 低于它：验证码可选（required=false）。达到它：required=true，
	// 登录必须携带验证码。任何一次登录成功都会清零计数。
	CaptchaFailureThreshold = 3

	// captchaAlphabet 刻意剔除 0/O/1/I/L 等易混字符。
	captchaAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
)

// ── 存储 ───────────────────────────────────────────────────────────────────

type captchaEntry struct {
	answerHash string    // sha256(规范化后的答案)，恒不存明文
	expiresAt  time.Time // 过期时刻
	attempts   int       // 已提交次数（含本次）
}

// captchaStore 是并发安全的验证码存储。
type captchaStore struct {
	mu      sync.Mutex
	entries map[string]*captchaEntry
}

func newCaptchaStore() *captchaStore {
	return &captchaStore{entries: make(map[string]*captchaEntry)}
}

// store 签发一个验证码，返回 captchaID 与答案明文。
// 答案只在这里出现一次，交给渲染函数后即丢弃。
func (s *captchaStore) store() (string, string, bool) {
	id, err := randomToken(16)
	if err != nil {
		return "", "", false
	}
	answer, err := randomString(captchaAlphabet, CaptchaLength)
	if err != nil {
		return "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if len(s.entries) >= CaptchaMaxEntries {
		// 清理后仍超上限：拒发。不覆盖已有条目（避免把别人的验证码顶掉）。
		return "", "", false
	}
	s.entries[id] = &captchaEntry{
		answerHash: hashCaptchaAnswer(answer),
		expiresAt:  time.Now().Add(CaptchaTTL),
	}
	return id, answer, true
}

// verify 校验一次提交。**无论成功失败都删除该条目**（一次性语义）。
func (s *captchaStore) verify(id, code string) captchaVerdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[id]
	if !ok {
		return captchaMissing
	}
	// 一次性：先摘掉，后面无论走哪个分支都不会留下可重放的条目。
	delete(s.entries, id)
	if time.Now().After(entry.expiresAt) {
		return captchaExpired
	}
	entry.attempts++
	if entry.attempts > CaptchaMaxAttempts {
		return captchaExhausted
	}
	if subtle.ConstantTimeCompare(
		[]byte(entry.answerHash),
		[]byte(hashCaptchaAnswer(code)),
	) == 1 {
		return captchaOK
	}
	return captchaMismatch
}

// pruneLocked 清除过期条目。调用方必须已持锁。
func (s *captchaStore) pruneLocked() {
	now := time.Now()
	for id, e := range s.entries {
		if now.After(e.expiresAt) {
			delete(s.entries, id)
		}
	}
}

type captchaVerdict int

const (
	captchaOK captchaVerdict = iota
	captchaMissing
	captchaExpired
	captchaExhausted
	captchaMismatch
)

// ── 连续失败计数 ────────────────────────────────────────────────────────────

// loginFailureCounter 记录每个来源 IP 的连续登录失败次数。
// 成功一次即清零。用途是决定验证码是否**强制**（见 CaptchaFailureThreshold）。
type loginFailureCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func newLoginFailureCounter() *loginFailureCounter {
	return &loginFailureCounter{counts: make(map[string]int)}
}

func (c *loginFailureCounter) fail(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[key]++
}

func (c *loginFailureCounter) reset(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.counts, key)
}

// consecutive 返回当前连续失败次数。
func (c *loginFailureCounter) consecutive(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[key]
}

// required 表示该来源当前是否必须提交验证码。
func (c *loginFailureCounter) required(key string) bool {
	return c.consecutive(key) >= CaptchaFailureThreshold
}

// size 供测试断言内存有界。
func (c *loginFailureCounter) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.counts)
}

// ── 包级单例 ───────────────────────────────────────────────────────────────

var (
	captchaStoreGlobal      = newCaptchaStore()
	loginFailuresGlobal     = newLoginFailureCounter()
	captchaRateLimiterCache = NewRateLimiter(20, time.Minute) // 签发端点自身的限速
)

// ── 工具 ───────────────────────────────────────────────────────────────────

func randomToken(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// randomString 从 alphabet 中等概率取 n 个字符。用 crypto/rand +
// rejection sampling，避免 `rand.Intn` 取模带来的偏置。
func randomString(alphabet string, n int) (string, error) {
	var sb strings.Builder
	sb.Grow(n)
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		sb.WriteByte(alphabet[idx.Int64()])
	}
	return sb.String(), nil
}

// hashCaptchaAnswer 归一化（小写 + 去空白）后取 sha256。
// 归一化让人输 "a b c d" 也能过——不牺牲任何安全性。
func hashCaptchaAnswer(code string) string {
	normalized := strings.ToLower(strings.TrimSpace(code))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// ── PNG 渲染 ───────────────────────────────────────────────────────────────

// glyph5x7 是内建 5×7 点阵字体，列优先：每列一个字节，bit0 = 最上一行。
//
// 只覆盖 captchaAlphabet 里出现的字符（2-9 与除 L/O/I 外的部分大写字母），
// 外加几个高频易混字形做冗余。字模数据直接进像素缓冲，**不经过任何文本节点**，
// 所以响应里不存在机器可直读的答案（这正是不能用 SVG <text> 的原因，
// 见文件头第 2 条）。
var glyph5x7 = map[byte][5]byte{
	'2': {0x42, 0x61, 0x51, 0x49, 0x46},
	'3': {0x21, 0x41, 0x45, 0x4b, 0x31},
	'4': {0x18, 0x14, 0x12, 0x7f, 0x10},
	'5': {0x27, 0x45, 0x45, 0x45, 0x39},
	'6': {0x3c, 0x4a, 0x49, 0x49, 0x30},
	'7': {0x01, 0x71, 0x09, 0x05, 0x03},
	'8': {0x36, 0x49, 0x49, 0x49, 0x36},
	'9': {0x06, 0x49, 0x49, 0x29, 0x1e},
	'A': {0x7e, 0x11, 0x11, 0x11, 0x7e},
	'B': {0x7f, 0x49, 0x49, 0x49, 0x36},
	'C': {0x3e, 0x41, 0x41, 0x41, 0x22},
	'D': {0x7f, 0x41, 0x41, 0x22, 0x1c},
	'E': {0x7f, 0x49, 0x49, 0x49, 0x41},
	'F': {0x7f, 0x09, 0x09, 0x01, 0x01},
	'G': {0x3e, 0x41, 0x41, 0x51, 0x32},
	'H': {0x7f, 0x08, 0x08, 0x08, 0x7f},
	'J': {0x20, 0x40, 0x41, 0x3f, 0x01},
	'K': {0x7f, 0x08, 0x14, 0x22, 0x41},
	'M': {0x7f, 0x02, 0x04, 0x02, 0x7f},
	'N': {0x7f, 0x04, 0x08, 0x10, 0x7f},
	'P': {0x7f, 0x09, 0x09, 0x09, 0x06},
	'Q': {0x3e, 0x41, 0x51, 0x21, 0x5e},
	'R': {0x7f, 0x09, 0x19, 0x29, 0x46},
	'S': {0x46, 0x49, 0x49, 0x49, 0x31},
	'T': {0x01, 0x01, 0x7f, 0x01, 0x01},
	'U': {0x3f, 0x40, 0x40, 0x40, 0x3f},
	'V': {0x1f, 0x20, 0x40, 0x20, 0x1f},
	'W': {0x7f, 0x20, 0x18, 0x20, 0x7f},
	'X': {0x63, 0x14, 0x08, 0x14, 0x63},
	'Y': {0x03, 0x04, 0x78, 0x04, 0x03},
	'Z': {0x61, 0x51, 0x49, 0x45, 0x43},
}

// renderCaptchaPNG 把答案逐像素画成 PNG，并返回可直接塞进 <img src> 的
// data URI。
//
// 抗机器识别的着力点：
//   - 字形是位图，不是文本 → 无法用正则/解析器直读（这是最关键的一条）；
//   - 每个字符独立的纵向抖动 + 颜色 + 间距抖动；
//   - 叠加干扰线与背景噪点。
//
// 刻意**不做**强扭曲（字符粘连/严重倾斜）：验证码的主要用户是正常的管理员，
// 读不出来会直接变成支持工单，而强扭曲挡不住任何真正的 OCR。
func renderCaptchaPNG(answer string) string {
	const (
		width   = 140
		height  = 48
		scale   = 5 // 5x7 字模放大 5 倍 → 25x35
		gapBase = 6
	)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// 背景：浅灰蓝，接近登录卡片底色。
	bg := color.RGBA{R: 0xf2, G: 0xf5, B: 0xf9, A: 0xff}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, bg)
		}
	}

	randInt := func(n int64) int64 {
		v, err := rand.Int(rand.Reader, big.NewInt(n))
		if err != nil {
			return 0
		}
		return v.Int64()
	}

	// 背景噪点。
	for i := 0; i < 400; i++ {
		g := uint8(0xd0 + randInt(40))
		img.Set(int(randInt(int64(width))), int(randInt(int64(height))), color.RGBA{R: g, G: g, B: g, A: 0xff})
	}

	// 字符。
	penX := (width - (len(answer)*(5*scale+gapBase) - gapBase)) / 2
	if penX < 2 {
		penX = 2
	}
	topPad := (height - 7*scale) / 2
	for _, ch := range answer {
		glyph, ok := glyph5x7[byte(ch)]
		if !ok {
			// 字符表漏了字形：宁可画成方块也不要静默少画一个字符
			// （少画会让两个不同答案渲染成同一张图，验证码强度归零）。
			glyph = [5]byte{0x7f, 0x41, 0x41, 0x41, 0x7f}
		}
		dy := int(randInt(9)) - 4 // -4..+4
		fill := color.RGBA{
			R: uint8(0x10 + randInt(0x60)),
			G: uint8(0x20 + randInt(0x60)),
			B: uint8(0x50 + randInt(0x80)),
			A: 0xff,
		}
		for col := 0; col < 5; col++ {
			bits := glyph[col]
			for row := 0; row < 7; row++ {
				if bits&(1<<uint(row)) == 0 {
					continue
				}
				for dy2 := 0; dy2 < scale; dy2++ {
					for dx := 0; dx < scale; dx++ {
						px := penX + col*scale + dx
						py := topPad + row*scale + dy2 + dy
						if px >= 0 && px < width && py >= 0 && py < height {
							img.Set(px, py, fill)
						}
					}
				}
			}
		}
		penX += 5*scale + gapBase + int(randInt(6))
	}

	// 干扰线：盖在字符上，制造轻度阅读阻力。
	for i := 0; i < 3; i++ {
		lc := color.RGBA{R: uint8(0x80 + randInt(0x70)), G: uint8(0x60 + randInt(0x60)), B: uint8(0x40 + randInt(0x60)), A: 0xc0}
		x1, y1 := int(randInt(int64(width))), int(randInt(int64(height)))
		x2, y2 := int(randInt(int64(width))), int(randInt(int64(height)))
		drawLine(img, x1, y1, x2, y2, lc)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// drawLine 用 Bresenham 画一条线（仅测试与实现自洽，不追求抗锯齿）。
func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	for {
		if x0 >= 0 && x0 < img.Rect.Dx() && y0 >= 0 && y0 < img.Rect.Dy() {
			img.Set(x0, y0, c)
		}
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// ── Handler ────────────────────────────────────────────────────────────────

// handleCaptcha 签发一张验证码。公开端点（无需登录）。
//
// 响应：{"captcha_id","image","expires_at","required","ttl_seconds"}
//
// `image` 是 data:image/png;base64 的位图验证码。
// `required` 告诉前端「当前这个 IP 已被强制要求验证码」——前端据此把输入框
// 标成必填并在未填时拦下提交，避免用户提交后才收到 400。
func (h *Handler) handleCaptcha(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	clientIP := clientIPFromRequest(r)
	if !captchaRateLimiterCache.Allow(clientIP) {
		writeError(w, http.StatusTooManyRequests, "too many captcha requests, try again later")
		return
	}

	id, answer, ok := captchaStoreGlobal.store()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "captcha service busy, try again later")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"captcha_id":  id,
		"image":       renderCaptchaPNG(answer),
		"expires_at":  time.Now().Add(CaptchaTTL).Format(time.RFC3339),
		"required":    loginFailuresGlobal.required(clientIP),
		"ttl_seconds": int(CaptchaTTL.Seconds()),
	})
}

// verifyLoginCaptcha 是 handleLogin 的闸门。
//
// 返回值 ok=false 时已经写过响应体，调用方直接 return。
//
// 判定顺序刻意如此：
//   - required 且没带 → 400 captcha_required（前端据此把输入框变必填并刷新验证码）
//   - 带了但对不上   → 400 captcha_invalid（**不**暴露「不存在/过期/次数用尽」的
//     区别，那等于给枚举提供 oracle；审计日志里才记明文原因）
//   - 没 required 且没带 → 放行（自适应：正常用户不必被打扰）
func (h *Handler) verifyLoginCaptcha(w http.ResponseWriter, clientIP, captchaID, captchaCode string) bool {
	required := loginFailuresGlobal.required(clientIP)
	if captchaID == "" && captchaCode == "" {
		if required {
			writeErrorWithCode(w, http.StatusBadRequest, "captcha_required", "captcha required after repeated failed logins")
			return false
		}
		return true
	}
	if captchaID == "" || captchaCode == "" {
		writeErrorWithCode(w, http.StatusBadRequest, "captcha_invalid", "captcha answer is incomplete")
		return false
	}
	verdict := captchaStoreGlobal.verify(captchaID, captchaCode)
	if verdict == captchaOK {
		return true
	}
	reason := "mismatch"
	switch verdict {
	case captchaMissing:
		reason = "missing"
	case captchaExpired:
		reason = "expired"
	case captchaExhausted:
		reason = "attempts_exhausted"
	}
	h.auditLog("", "authentication.captcha_rejected", "user", 0, map[string]any{
		"ip": clientIP, "captcha_id": captchaID, "reason": reason,
	})
	writeErrorWithCode(w, http.StatusBadRequest, "captcha_invalid", "captcha answer is incorrect or expired")
	return false
}

// recordLoginFailure / clearLoginFailures 是 handleLogin 的记账点。
func recordLoginFailure(clientIP string) { loginFailuresGlobal.fail(clientIP) }
func clearLoginFailures(clientIP string) { loginFailuresGlobal.reset(clientIP) }