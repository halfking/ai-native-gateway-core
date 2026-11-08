package admin

// sales_public_handler.go —— 报价分享的**唯一公开面**（W1-T2，规划 §5）：
// GET /api/public/quote/{token} —— 无登录、只读、**真限速**。
//
// 公开面安全审查（规划 §5 + 风暴档 §8-3 裁决：公开无登录 = TTL+撤销+审计）：
//   - token 128bit 随机、明文不落库（表里只有 sha256）⇒ 无枚举面；
//   - 查找 = hash 精确命中，TTL 过期/已撤销 ⇒ 404（不泄露状态语义）；
//   - 访问记审计（viewed/opened + detail jsonb 逐次访问；IP 走 origin
//     中间件的可信解析，裸 XFF 首段仅作无中间件时的兜底——P3-11）；
//   - **per-IP 固定窗限速**（2026-11-08 审计 P2-2 修复：此前注释声称限速
//     但实现为零——匿名可达的写放大面）：阈值 = settings
//     sales.public.rate_limit_per_min（默认 30 次/分/IP），超限 429；
//   - 响应体 = 客户安全投影（sales.PublicQuote）：只含模型展示名/量/销售
//     价/折扣 —— **成本/毛利/供应商身份/凭据物理不进结构体**（编译期锁：
//     结构体没有这些字段，响应体断言测试再钉一层 JSON 面）。

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/httpx"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/sales"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// ipWindowLimiter is a fixed-window per-key rate limiter (in-memory, single
// process). Enough for one public read endpoint; not a general-purpose limiter.
type ipWindowLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	buckets map[string]*ipWindowBucket
	maxKeys int // 粗防内存膨胀：匿名键空间有界，键数超限即整表重建
}

type ipWindowBucket struct {
	windowStart time.Time
	count       int
}

func newIPWindowLimiter() *ipWindowLimiter {
	return &ipWindowLimiter{window: time.Minute, buckets: map[string]*ipWindowBucket{}, maxKeys: 65536}
}

// allow reports whether key may proceed in the current window.
func (l *ipWindowLimiter) allow(key string, maxPerWindow int) bool {
	if maxPerWindow <= 0 {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) >= l.maxKeys {
		l.buckets = map[string]*ipWindowBucket{} // 粗防：整体重置优于无界增长
	}
	b, ok := l.buckets[key]
	if !ok || now.Sub(b.windowStart) >= l.window {
		l.buckets[key] = &ipWindowBucket{windowStart: now, count: 1}
		return true
	}
	b.count++
	return b.count <= maxPerWindow
}

// salesPublicLimiter is the process-wide limiter for the public quote face.
var salesPublicLimiter = newIPWindowLimiter()

// registerSalesPublicRoutes wires the single public share endpoint.
func (h *Handler) registerSalesPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/public/quote/", h.handleSalesPublicQuote)
}

func (h *Handler) handleSalesPublicQuote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// P2-2：真限速（每分钟/IP，settings 热改即生效）。IP 用 origin 中间件的
	// 可信解析（trusted-proxy allowlist 之后的 client_ip）——中间件不在链上
	// 时（单测/直连）才落到 RemoteAddr 兜底。
	ip := middleware.ContextClientIP(r.Context())
	if ip == "" {
		ip = clientIPForAudit(r)
	}
	maxPerMin := settings.GetPlatformInt("sales.public.rate_limit_per_min", 30)
	if !salesPublicLimiter.allow(ip, maxPerMin) {
		w.Header().Set("Retry-After", strconv.Itoa(60))
		writeError(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	svc := h.salesSvc()
	if svc == nil || !svc.Enabled() {
		// 公开面对不可用后端同样 404：不向匿名调用方区分「没配库」与「没这单」。
		http.NotFound(w, r)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/api/public/quote/")
	token = strings.TrimSpace(token)
	if token == "" || strings.Contains(token, "/") || len(token) > 64 {
		http.NotFound(w, r)
		return
	}
	pq, err := svc.GetPublicQuote(r.Context(), token, clientIPForAudit(r), r.UserAgent())
	if err != nil {
		// expired / revoked / not-found / internal ⇒ 一律 404（不泄露区分度）。
		// A10（2026-11-05 审计）：内部错误必须落服务端日志（此前连 DB 故障
		// 都静默吞成 404，排障无入口）；响应面对匿名调用方维持不区分。
		if !errors.Is(err, sales.ErrNotFound) {
			slog.Error("sales public quote resolve failed", "caller", "sales_public_handler.go", "err", err)
		}
		http.NotFound(w, r)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", pq)
}

// clientIPForAudit 提取审计用客户端 IP（X-Forwarded-For 首段优先）。
// 仅在 origin 中间件不在链上时被限速兜底用到；正常链路上审计与限速都走
// middleware.ContextClientIP（trusted-proxy allowlist 之后才信 XFF——
// 2026-11-08 审计 P3-11：裸信 XFF 首段 = 匿名可伪造审计 IP）。
func clientIPForAudit(r *http.Request) string {
	if ip := middleware.ContextClientIP(r.Context()); ip != "" {
		return ip
	}
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		if i := strings.IndexByte(xf, ','); i > 0 {
			return strings.TrimSpace(xf[:i])
		}
		return strings.TrimSpace(xf)
	}
	return r.RemoteAddr
}
