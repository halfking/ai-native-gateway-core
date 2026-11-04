package admin

// sales_public_handler.go —— 报价分享的**唯一公开面**（W1-T2，规划 §5）：
// GET /api/public/quote/{token} —— 无登录、只读、限速。
//
// 公开面安全审查（规划 §5 + 风暴档 §8-3 裁决：公开无登录 = TTL+撤销+审计）：
//   - token 128bit 随机、明文不落库（表里只有 sha256）⇒ 无枚举面；
//   - 查找 = hash 精确命中，TTL 过期/已撤销 ⇒ 404（不泄露状态语义）；
//   - 访问记审计（viewed/opened + detail jsonb 逐次访问）；
//   - 响应体 = 客户安全投影（sales.PublicQuote）：只含模型展示名/量/销售
//     价/折扣 —— **成本/毛利/供应商身份/凭据物理不进结构体**（编译期锁：
//     结构体没有这些字段，响应体断言测试再钉一层 JSON 面）。

import (
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/httpx"
)

// registerSalesPublicRoutes wires the single public share endpoint.
func (h *Handler) registerSalesPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/public/quote/", h.handleSalesPublicQuote)
}

func (h *Handler) handleSalesPublicQuote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
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
		http.NotFound(w, r)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, "application/json", pq)
}

// clientIPForAudit 提取审计用客户端 IP（X-Forwarded-For 首段优先）。
func clientIPForAudit(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		if i := strings.IndexByte(xf, ','); i > 0 {
			return strings.TrimSpace(xf[:i])
		}
		return strings.TrimSpace(xf)
	}
	return r.RemoteAddr
}
