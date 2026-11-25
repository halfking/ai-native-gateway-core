package admin

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── 存储语义 ────────────────────────────────────────────────────────────────

func TestCaptchaStore_VerifySucceedsOnce(t *testing.T) {
	s := newCaptchaStore()
	id, answer, ok := s.store()
	if !ok {
		t.Fatal("store() refused to issue")
	}
	if got := s.verify(id, answer); got != captchaOK {
		t.Fatalf("first verify = %v, want captchaOK", got)
	}
	// 一次性：同一张图再提交一次必须是 missing，而不是再次通过。
	if got := s.verify(id, answer); got != captchaMissing {
		t.Fatalf("replay verify = %v, want captchaMissing", got)
	}
}

func TestCaptchaStore_MismatchIsRejectedAndConsumed(t *testing.T) {
	s := newCaptchaStore()
	id, answer, ok := s.store()
	if !ok {
		t.Fatal("store() refused to issue")
	}
	wrong := "ZZZZ"
	if wrong == answer {
		wrong = "YYYY" // 别测出「碰巧答对」
	}
	if got := s.verify(id, wrong); got != captchaMismatch {
		t.Fatalf("wrong answer = %v, want captchaMismatch", got)
	}
	// 错答之后即使用正确答案也不放行——否则验证码可被无限试错。
	if got := s.verify(id, answer); got != captchaMissing {
		t.Fatalf("verify after mismatch = %v, want captchaMissing", got)
	}
}

func TestCaptchaStore_AnswerIsCaseAndSpaceInsensitive(t *testing.T) {
	s := newCaptchaStore()
	id, answer, ok := s.store()
	if !ok {
		t.Fatal("store() refused to issue")
	}
	if got := s.verify(id, strings.ToLower(answer)+"  "); got != captchaOK {
		t.Fatalf("lowercased+trailing-space verify = %v, want captchaOK", got)
	}
}

func TestCaptchaStore_ExpiredIsRejected(t *testing.T) {
	s := newCaptchaStore()
	id, answer, ok := s.store()
	if !ok {
		t.Fatal("store() refused to issue")
	}
	s.mu.Lock()
	s.entries[id].expiresAt = time.Now().Add(-time.Second)
	s.mu.Unlock()

	if got := s.verify(id, answer); got != captchaExpired {
		t.Fatalf("expired verify = %v, want captchaExpired", got)
	}
}

// TestCaptchaStore_AttemptCap 覆盖「验证码自身不能被当口令爆破」。
// 第 CaptchaMaxAttempts 次提交必须 exhausted —— 即使那一次答案是对的。
// 少了这条，30^4 的字符表就成了免费字典。
func TestCaptchaStore_AttemptCap(t *testing.T) {
	s := newCaptchaStore()
	id, answer, ok := s.store()
	if !ok {
		t.Fatal("store() refused to issue")
	}
	// 把这张图的已用次数顶到上限：下一次提交会用尽额度，于是
	// 「即使答对也必须被拒」——少了这条，31^4 的字符表就成了免费字典。
	s.mu.Lock()
	s.entries[id].attempts = CaptchaMaxAttempts
	s.mu.Unlock()
	if got := s.verify(id, answer); got != captchaExhausted {
		t.Fatalf("over-cap verify = %v, want captchaExhausted", got)
	}

	// 反向对照：次数没顶满时正确答案必须放行（否则这条判据就是恒红的假阳性）。
	freshID, freshAnswer, ok := s.store()
	if !ok {
		t.Fatal("store() refused to issue")
	}
	if got := s.verify(freshID, freshAnswer); got != captchaOK {
		t.Fatalf("fresh verify = %v, want captchaOK", got)
	}
}

func TestCaptchaStore_EntryCapRefusesInsteadOfGrowing(t *testing.T) {
	s := newCaptchaStore()
	for i := 0; i < CaptchaMaxEntries; i++ {
		if _, _, ok := s.store(); !ok {
			t.Fatalf("issue %d refused below cap", i)
		}
	}
	if _, _, ok := s.store(); ok {
		t.Fatal("issued past CaptchaMaxEntries — unbounded growth is a live OOM path")
	}
	s.mu.Lock()
	n := len(s.entries)
	s.mu.Unlock()
	if n != CaptchaMaxEntries {
		t.Fatalf("entries = %d, want capped at %d", n, CaptchaMaxEntries)
	}
}

func TestCaptchaStore_PruneDropsExpired(t *testing.T) {
	s := newCaptchaStore()
	id, _, ok := s.store()
	if !ok {
		t.Fatal("store() refused to issue")
	}
	s.mu.Lock()
	s.entries[id].expiresAt = time.Now().Add(-time.Minute)
	s.pruneLocked()
	n := len(s.entries)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("entries after prune = %d, want 0", n)
	}
}

// TestCaptchaAlphabet_CoversEveryGlyph 锁住「字符表里有字模」。
// 漏一个字模不会编译失败、不会 panic，只会画出一个方块 —— 也就是两个不同
// 答案可能渲染成同一张图，验证码强度悄悄归零。这条判据让那种退化变成红灯。
func TestCaptchaAlphabet_CoversEveryGlyph(t *testing.T) {
	for i := 0; i < len(captchaAlphabet); i++ {
		if _, ok := glyph5x7[captchaAlphabet[i]]; !ok {
			t.Errorf("captchaAlphabet[%d]=%q has no 5x7 glyph", i, captchaAlphabet[i])
		}
	}
	if len(captchaAlphabet) != 31 {
		t.Fatalf("captchaAlphabet length = %d, want 31", len(captchaAlphabet))
	}
}

// ── 连续失败计数 ────────────────────────────────────────────────────────────

func TestLoginFailureCounter_ThresholdAndReset(t *testing.T) {
	c := newLoginFailureCounter()
	for i := 1; i < CaptchaFailureThreshold; i++ {
		c.fail("1.2.3.4")
		if c.required("1.2.3.4") {
			t.Fatalf("required became true after only %d failures", i)
		}
	}
	c.fail("1.2.3.4")
	if !c.required("1.2.3.4") {
		t.Fatal("required should be true at the threshold")
	}
	c.reset("1.2.3.4")
	if c.required("1.2.3.4") {
		t.Fatal("required must go false after a successful login")
	}
	if c.size() != 0 {
		t.Fatalf("counter size after reset = %d, want 0", c.size())
	}
}

func TestLoginFailureCounter_IsolatesClients(t *testing.T) {
	c := newLoginFailureCounter()
	for i := 0; i < CaptchaFailureThreshold; i++ {
		c.fail("attacker")
	}
	if !c.required("attacker") {
		t.Fatal("setup: attacker should be required")
	}
	if c.required("innocent") {
		t.Fatal("one IP's failures must not impose a captcha on another IP")
	}
}

// ── 失败计数的存活窗口（TTL）────────────────────────────────────────────────
//
// ★ 这组判据钉的是 2026-11-25 第 30 轮的**候选实现**（★ 待 owner 放行 ★）：
//   在加窗口之前，失败计数**永不过期** ⇒ 而 captcha_required 时走不到密码校验
//   （闸门在 bcrypt 之前）⇒ **成功不可达** ⇒ 计数永不清 ⇒ 该 IP 被永久锁死。
//   ⇒ 加了窗口后：★ 过期即视为 0（闸门会自愈）、★ 过期后的新失败**从头计**
//   （不是接着旧计数加）、★ 窗口是**滑动**的（每次失败都把起点往后推）。
// ★ 用**假时钟**注入（★ 计数器暴露 now 字段 ★）⇒ 不必真等 15 分钟。

// newTestFailureCounter 返回一个计数器 ＋ 它的假时钟指针。
func newTestFailureCounter() (*loginFailureCounter, *time.Time) {
	c := newLoginFailureCounter()
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	return c, &now
}

// TestCaptchaFailureTTL_IsPinnedTo15Minutes 把**窗口长度本身**钉死。
// ★ 为什么要有这条：★ 下面那组 TTL 判据都是**相对 CaptchaFailureTTL** 写的
//   （★ 假时钟按常量推进 ★）⇒ ⇒ **改常量不会让它们变红** ⇒ ⇒ 窗口长度被悄悄改成
//   1 分钟或 24 小时时，**没有任何判据会叫**。
//   ⇒ ⇒ 所以窗口长度必须**单独钉一条**：★ 改它＝改判据 ⇒ 必须显式改这条测试 ★★。
func TestCaptchaFailureTTL_IsPinnedTo15Minutes(t *testing.T) {
	if CaptchaFailureTTL != 15*time.Minute {
		t.Fatalf("CaptchaFailureTTL = %v, want 15m（★ 窗口长度是被显式决定的：★ 改它必须同时改这条判据 ★）", CaptchaFailureTTL)
	}
}

func TestLoginFailureCounter_TTLExpiresAndSelfHeals(t *testing.T) {
	c, now := newTestFailureCounter()
	for i := 0; i < CaptchaFailureThreshold; i++ {
		c.fail("1.2.3.4")
	}
	if !c.required("1.2.3.4") {
		t.Fatal("setup: at threshold the captcha must be required")
	}
	// 窗口内：仍然锁着
	*now = now.Add(CaptchaFailureTTL - time.Minute)
	if !c.required("1.2.3.4") {
		t.Fatal("inside the TTL window the captcha must still be required")
	}
	// 窗口外：★ 自愈 ★（这是这次改动的全部意义）
	*now = now.Add(2 * time.Minute)
	if c.required("1.2.3.4") {
		t.Fatal("after the TTL the counter must expire ⇒ the captcha must NOT be required")
	}
	if got := c.consecutive("1.2.3.4"); got != 0 {
		t.Fatalf("consecutive after TTL = %d, want 0", got)
	}
	if got := c.size(); got != 0 {
		t.Fatalf("counter size after TTL = %d, want 0（过期条目必须被剪掉 ⇒ 内存有界）", got)
	}
}

func TestLoginFailureCounter_TTLDoesNotAccumulateAcrossWindows(t *testing.T) {
	c, now := newTestFailureCounter()
	c.fail("1.2.3.4")
	c.fail("1.2.3.4")
	if got := c.consecutive("1.2.3.4"); got != 2 {
		t.Fatalf("consecutive = %d, want 2", got)
	}
	// 过期 ⇒ 新的一次失败必须**从头计**（不是 2+1=3 ⇒ 否则又锁死）
	*now = now.Add(CaptchaFailureTTL + time.Minute)
	c.fail("1.2.3.4")
	if got := c.consecutive("1.2.3.4"); got != 1 {
		t.Fatalf("consecutive after a stale window = %d, want 1（过期后必须从头计）", got)
	}
	if c.required("1.2.3.4") {
		t.Fatal("a single failure in a fresh window must not require a captcha")
	}
}

func TestLoginFailureCounter_WindowSlidesOnEachFailure(t *testing.T) {
	c, now := newTestFailureCounter()
	c.fail("1.2.3.4")
	// 滑到窗口边缘（但仍在窗口内）
	*now = now.Add(CaptchaFailureTTL - time.Minute)
	c.fail("1.2.3.4")
	if got := c.consecutive("1.2.3.4"); got != 2 {
		t.Fatalf("consecutive = %d, want 2（窗口内连续失败应当累加）", got)
	}
	// ★ 再过「原窗口长度」也不该过期：★ 因为最后一次失败把窗口起点往后推了
	*now = now.Add(CaptchaFailureTTL - time.Minute)
	if got := c.consecutive("1.2.3.4"); got != 2 {
		t.Fatalf("consecutive = %d, want 2（★ 窗口是滑动的：最后一次失败刷新了起点 ★）", got)
	}
}

func TestLoginFailureCounter_ResetClearsBothMaps(t *testing.T) {
	c, _ := newTestFailureCounter()
	c.fail("1.2.3.4")
	c.fail("1.2.3.4")
	c.reset("1.2.3.4")
	if got := c.consecutive("1.2.3.4"); got != 0 {
		t.Fatalf("consecutive after reset = %d, want 0", got)
	}
	if got := c.size(); got != 0 {
		t.Fatalf("counter size after reset = %d, want 0", got)
	}
	// 复位后的一次失败必须从 1 起算（★ 不能继承复位前的计数 ★）
	c.fail("1.2.3.4")
	if got := c.consecutive("1.2.3.4"); got != 1 {
		t.Fatalf("consecutive after reset+fail = %d, want 1", got)
	}
}

// ── PNG 渲染 ────────────────────────────────────────────────────────────────

// TestRenderCaptchaPNG_NoPlaintextAnswer 是本文件最关键的一条判据。
//
// SVG 版本把答案写进 <text> 节点，脚本 `grep '<text'` 就能白拿答案。
// 这条判据模拟那个攻击：拿签发时的明文答案去搜整个响应体，一个字节都不该命中。
func TestRenderCaptchaPNG_NoPlaintextAnswer(t *testing.T) {
	const answer = "AB39"
	dataURI := renderCaptchaPNG(answer)
	if dataURI == "" {
		t.Fatal("renderCaptchaPNG returned empty")
	}
	if !strings.HasPrefix(dataURI, "data:image/png;base64,") {
		t.Fatalf("not a PNG data URI: %.40q", dataURI)
	}
	// 去掉 base64 载荷后，剩下的前后缀里也不能有答案。
	prefix := "data:image/png;base64,"
	payload := strings.TrimPrefix(dataURI, prefix)
	if strings.Contains(payload, answer) {
		t.Fatal("the base64 payload literally contains the answer")
	}
	// 上面的断言对 base64 而言其实很弱（编码后一般不再含原文），
	// 所以再补一条：**解码后的 PNG 字节里也不该出现答案的 ASCII 串**，
	// 更不能出现 SVG <text> 这类文本节点。
	decoded := decodeBase64(t, payload)
	if strings.Contains(string(decoded), answer) {
		t.Fatal("decoded PNG bytes contain the plaintext answer")
	}
	if strings.Contains(string(decoded), "<text") {
		t.Fatal("decoded payload contains an SVG <text> node — the answer would be machine-readable")
	}
	// 必须是真 PNG（magic number）。
	if len(decoded) < 8 || string(decoded[1:4]) != "PNG" {
		t.Fatal("payload is not a PNG")
	}
}

func TestRenderCaptchaPNG_NeverEmpty(t *testing.T) {
	// 抽样验证渲染不会因为随机分支而返回空串（空串在前端就是一个坏图）。
	for i := 0; i < 50; i++ {
		answer, err := randomString(captchaAlphabet, CaptchaLength)
		if err != nil {
			t.Fatal(err)
		}
		if renderCaptchaPNG(answer) == "" {
			t.Fatalf("empty render for %q", answer)
		}
	}
}

// ── Handler ─────────────────────────────────────────────────────────────────

func TestHandleCaptcha_IssuesUsableChallenge(t *testing.T) {
	resetCaptchaSingletonsForTest()
	h := &Handler{}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/captcha", nil)
	req.RemoteAddr = "10.0.0.9:1234"
	h.handleCaptcha(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store (a cached captcha is a stale captcha)", cc)
	}

	var resp struct {
		CaptchaID  string `json:"captcha_id"`
		Image      string `json:"image"`
		Required   bool   `json:"required"`
		TTLSeconds int    `json:"ttl_seconds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if resp.CaptchaID == "" || resp.Image == "" {
		t.Fatalf("incomplete challenge: id=%q image_len=%d", resp.CaptchaID, len(resp.Image))
	}
	if resp.TTLSeconds != int(CaptchaTTL.Seconds()) {
		t.Fatalf("ttl_seconds = %d, want %d", resp.TTLSeconds, int(CaptchaTTL.Seconds()))
	}
	// 全新客户端不该被强制。
	if resp.Required {
		t.Fatal("required=true for a client with no failures")
	}
	// 签发出来的 id 必须真的能过（防止 handler 与 store 各用各的状态）。
	if got := captchaStoreGlobal.verify(resp.CaptchaID, "ZZZZ"); got == captchaMissing {
		t.Fatal("issued captcha_id is not present in the store")
	}
}

func TestHandleCaptcha_RequiredAfterRepeatedFailures(t *testing.T) {
	resetCaptchaSingletonsForTest()
	h := &Handler{}
	// 失败计数按 clientIPFromRequest 契约以剥端口后的 IP 分桶；
	// RemoteAddr 故意带端口，钉住「handler 查找前先剥端口」。
	for i := 0; i < CaptchaFailureThreshold; i++ {
		loginFailuresGlobal.fail("10.0.0.9")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/captcha", nil)
	req.RemoteAddr = "10.0.0.9:1234"
	h.handleCaptcha(rec, req)

	var resp struct {
		Required bool `json:"required"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Required {
		t.Fatal("required=false after reaching the failure threshold — the UI would let the user submit and eat a 400")
	}
}

func TestHandleCaptcha_RejectsBadMethod(t *testing.T) {
	resetCaptchaSingletonsForTest()
	h := &Handler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/auth/captcha", nil)
	h.handleCaptcha(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// ── 登录闸门 ────────────────────────────────────────────────────────────────

func TestVerifyLoginCaptcha_OptionalBelowThreshold(t *testing.T) {
	resetCaptchaSingletonsForTest()
	h := &Handler{}
	rec := httptest.NewRecorder()
	// 未达阈值且没带验证码 → 放行（正常用户不该被打扰）。
	if !h.verifyLoginCaptcha(rec, "10.0.0.1", "", "") {
		t.Fatalf("blocked a first-time login: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("gate wrote a response on the pass-through path: %s", rec.Body.String())
	}
}

func TestVerifyLoginCaptcha_RequiredAfterThreshold(t *testing.T) {
	resetCaptchaSingletonsForTest()
	h := &Handler{}
	for i := 0; i < CaptchaFailureThreshold; i++ {
		loginFailuresGlobal.fail("10.0.0.2")
	}
	rec := httptest.NewRecorder()
	if h.verifyLoginCaptcha(rec, "10.0.0.2", "", "") {
		t.Fatal("login without a captcha passed after reaching the threshold")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "captcha_required") {
		t.Fatalf("body = %s, want error_code captcha_required so the UI can react", rec.Body.String())
	}
}

func TestVerifyLoginCaptcha_WrongAnswerRejectedEvenWhenOptional(t *testing.T) {
	resetCaptchaSingletonsForTest()
	h := &Handler{}
	id, _, ok := captchaStoreGlobal.store()
	if !ok {
		t.Fatal("store() refused to issue")
	}
	rec := httptest.NewRecorder()
	// 未达阈值，但**带了**错答案：必须拒。
	// 放行的话，「验证码是可选的」就会被读成「验证码可以不填也可以随便填」。
	if h.verifyLoginCaptcha(rec, "10.0.0.3", id, "ZZZZ") {
		t.Fatal("a wrong captcha was accepted")
	}
	if !strings.Contains(rec.Body.String(), "captcha_invalid") {
		t.Fatalf("body = %s, want error_code captcha_invalid", rec.Body.String())
	}
}

func TestVerifyLoginCaptcha_CorrectAnswerPassesWhenRequired(t *testing.T) {
	resetCaptchaSingletonsForTest()
	h := &Handler{}
	for i := 0; i < CaptchaFailureThreshold; i++ {
		loginFailuresGlobal.fail("10.0.0.4")
	}
	id, answer, ok := captchaStoreGlobal.store()
	if !ok {
		t.Fatal("store() refused to issue")
	}
	rec := httptest.NewRecorder()
	if !h.verifyLoginCaptcha(rec, "10.0.0.4", id, answer) {
		t.Fatalf("a correct captcha was rejected: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestVerifyLoginCaptcha_HalfFilledAnswerRejected(t *testing.T) {
	resetCaptchaSingletonsForTest()
	h := &Handler{}
	id, _, ok := captchaStoreGlobal.store()
	if !ok {
		t.Fatal("store() refused to issue")
	}
	for _, tc := range []struct{ id, code string }{
		{id, ""}, // 有 id 没答案
		{"", "ABCD"},
	} {
		rec := httptest.NewRecorder()
		if h.verifyLoginCaptcha(rec, "10.0.0.5", tc.id, tc.code) {
			t.Fatalf("half-filled captcha (id=%q code=%q) was accepted", tc.id, tc.code)
		}
	}
}

func TestVerifyLoginCaptcha_ErrorBodyDoesNotDistinguishFailureModes(t *testing.T) {
	// 区分「不存在 / 过期 / 次数用尽 / 答错」等于给枚举提供 oracle。
	// 四种失败必须返回**同一段**文案。
	resetCaptchaSingletonsForTest()
	h := &Handler{}

	var bodies []string
	rec := httptest.NewRecorder()
	h.verifyLoginCaptcha(rec, "10.0.0.6", "deadbeef", "ABCD") // missing
	bodies = append(bodies, rec.Body.String())

	id, answer, _ := captchaStoreGlobal.store()
	rec = httptest.NewRecorder()
	h.verifyLoginCaptcha(rec, "10.0.0.6", id, "ZZZZ") // mismatch
	bodies = append(bodies, rec.Body.String())

	id, answer, _ = captchaStoreGlobal.store()
	captchaStoreGlobal.mu.Lock()
	captchaStoreGlobal.entries[id].expiresAt = time.Now().Add(-time.Second)
	captchaStoreGlobal.mu.Unlock()
	rec = httptest.NewRecorder()
	h.verifyLoginCaptcha(rec, "10.0.0.6", id, answer) // expired
	bodies = append(bodies, rec.Body.String())

	for i := 1; i < len(bodies); i++ {
		if bodies[i] != bodies[0] {
			t.Fatalf("failure modes leak through distinguishable bodies:\n  %s\n  %s", bodies[0], bodies[i])
		}
	}
}

// ── 保持登录 cookie ─────────────────────────────────────────────────────────

func TestSetSessionCookieRemember_FalseIsSessionScoped(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/token", nil)
	expires := time.Now().Add(24 * time.Hour)
	setSessionCookieRemember(rec, req, "jwt-token", expires, false)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count = %d, want 1", len(cookies))
	}
	c := cookies[0]
	// Go 的 net/http：MaxAge<0 才会输出 "Max-Age=0"（删除语义）；
	// MaxAge==0 表示「不设置该属性」，正是会话级 cookie 需要的。
	if c.MaxAge != 0 {
		t.Fatalf("Max-Age = %d, want 0 (session cookie)", c.MaxAge)
	}
	if !c.Expires.IsZero() {
		t.Fatalf("Expires = %v, want zero (session cookie)", c.Expires)
	}
	if got := rec.Header().Get("Set-Cookie"); strings.Contains(got, "Max-Age") {
		t.Fatalf("Set-Cookie still carries Max-Age: %s", got)
	}
	// 安全属性一个都不能少。
	if !c.HttpOnly || c.Name != CookieName || c.Path != CookiePath {
		t.Fatalf("session cookie lost its hardening: %+v", c)
	}
}

func TestSetSessionCookieRemember_TrueIsPersistent(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/token", nil)
	expires := time.Now().Add(24 * time.Hour)
	setSessionCookieRemember(rec, req, "jwt-token", expires, true)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count = %d, want 1", len(cookies))
	}
	c := cookies[0]
	if c.MaxAge <= 0 {
		t.Fatalf("Max-Age = %d, want a positive persistence window", c.MaxAge)
	}
	if c.Expires.IsZero() {
		t.Fatal("Expires must be set for a persistent cookie")
	}
	if !c.HttpOnly {
		t.Fatal("HttpOnly lost")
	}
}

// TestSetSessionCookieRemember_TrueMatchesLegacyBehaviour 保证勾选「保持登录」
// 时与改动前的 setSessionCookie 逐字段一致 —— 即这个开关不能偷偷改变既有语义。
func TestSetSessionCookieRemember_TrueMatchesLegacyBehaviour(t *testing.T) {
	expires := time.Now().Add(24 * time.Hour)

	legacyRec := httptest.NewRecorder()
	setSessionCookie(legacyRec, httptest.NewRequest(http.MethodPost, "/api/auth/token", nil), "jwt-token", expires)

	rememberRec := httptest.NewRecorder()
	setSessionCookieRemember(rememberRec, httptest.NewRequest(http.MethodPost, "/api/auth/token", nil), "jwt-token", expires, true)

	// Max-Age 按秒取整，两次调用之间差 1 秒也是可能的，只比量级与属性。
	lc := legacyRec.Result().Cookies()[0]
	rc := rememberRec.Result().Cookies()[0]
	if lc.Name != rc.Name || lc.Path != rc.Path || lc.HttpOnly != rc.HttpOnly ||
		lc.Secure != rc.Secure || lc.SameSite != rc.SameSite || lc.Value != rc.Value {
		t.Fatalf("remember=true diverged from legacy cookie:\n  legacy=%+v\n  remember=%+v", lc, rc)
	}
	if diff := lc.MaxAge - rc.MaxAge; diff > 2 || diff < -2 {
		t.Fatalf("Max-Age diverged: legacy=%d remember=%d", lc.MaxAge, rc.MaxAge)
	}
}

// ── 辅助 ───────────────────────────────────────────────────────────────────

// resetCaptchaSingletonsForTest 把包级单例复位，避免测试之间互相污染
// （失败计数是全局的，不复位就会出现「上一个用例把本用例顶成 required」）。
func resetCaptchaSingletonsForTest() {
	captchaStoreGlobal = newCaptchaStore()
	loginFailuresGlobal = newLoginFailureCounter()
	captchaRateLimiterCache = NewRateLimiter(20, time.Minute)
}

// decodeBase64 解出 PNG 字节，供「答案不在载荷里」那条判据使用。
func decodeBase64(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	return raw
}
