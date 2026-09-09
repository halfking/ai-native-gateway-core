package freediscovery

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// URL 校验与端点安全工具 (2026-09-09 audit-fix)。
//
// 设计目标:
//   - 拒绝 SSRF 危险目标 (RFC1918 / loopback / link-local / IPv6 ULA / 私有 IP 字面量 /
//     cloud metadata、userinfo / 控制字符 / 非法 scheme / fragment);
//   - 限制 models_endpoint 必须是相对路径, 拒绝绝对 URL、`//host` 协议相对 URL;
//   - 配合 safehttpclient 的运行时 transport, 在校验层前置阻断, 测试允许注入 allowlist;
//   - 与生产 safehttpclient.NewWithAllowlist 的阻断范围对齐, 避免两套策略漂移.
//
// 注意: 本包不发起任何出站请求; 运行时 outbound 保护由 safehttpclient 提供.

// maxBaseURLLen 与 maxEndpointLen 防止异常长输入触发解析器开销.
const (
	maxBaseURLLen   = 2048
	maxEndpointLen  = 512
)

// isValidBaseURL 校验 base_url: 仅 http/https scheme, 拒绝 userinfo/fragment/控制字符,
// 拒绝 loopback/private/link-local/multicast/metadata 等危险 IP 字面量.
//
// 返回非空字符串表示错误 (与 isValidProviderCode 风格保持一致).
func isValidBaseURL(raw string) string {
	if raw == "" {
		return "base_url is required"
	}
	if len(raw) > maxBaseURLLen {
		return "base_url exceeds maximum length"
	}
	// 控制字符可能在 URL 解析器中被规范化, 提前阻断.
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "base_url contains control characters"
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Sprintf("base_url invalid: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "base_url must use http:// or https://"
	}
	host := u.Hostname()
	if host == "" {
		return "base_url must contain a hostname"
	}
	if u.User != nil {
		return "base_url must not contain userinfo (username:password@host)"
	}
	if u.Fragment != "" {
		return "base_url must not contain a fragment"
	}
	// IPv4/IPv6 字面量: 直接阻断私网地址段.
	if ip := net.ParseIP(host); ip != nil {
		if reason := blockReasonForIP(ip); reason != "" {
			return "base_url " + reason
		}
	}
	// 域名形式: 阻断常见绕过模式 (safehttpclient 在 dial 阶段会做进一步 DNS 校验).
	if strings.Contains(host, "@") {
		return "base_url hostname contains @ character (potential parser bypass)"
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return "base_url hostname contains whitespace"
	}
	return ""
}

// isValidModelsEndpoint 校验 models_endpoint: 必须以单斜杠开头 (相对路径),
// 拒绝 scheme、`//host` 协议相对 URL、含 userinfo 的绝对 URL 与控制字符.
func isValidModelsEndpoint(raw string) string {
	if raw == "" {
		// 默认值在调用处补 /models; 空串允许通过 (调用方填默认值).
		return ""
	}
	if len(raw) > maxEndpointLen {
		return "models_endpoint exceeds maximum length"
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "models_endpoint contains control characters"
		}
	}
	// 必须以单个 '/' 开头 (相对路径), 拒绝 '//' (协议相对 URL) 与任何 scheme.
	if !strings.HasPrefix(raw, "/") {
		return "models_endpoint must be a relative path starting with /"
	}
	if strings.HasPrefix(raw, "//") {
		return "models_endpoint must not be a scheme-relative URL (//host/path)"
	}
	// 进一步用 url.Parse 确认不解析成新 host.
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Sprintf("models_endpoint invalid: %v", err)
	}
	if u.Scheme != "" || u.Host != "" || u.User != nil {
		return "models_endpoint must be a relative path (no scheme/host/userinfo)"
	}
	return ""
}

// blockReasonForIP 返回 IP 命中拒绝策略的原因; 空串表示放行.
//
// 复用 safehttpclient 的常见阻断范围: loopback、private (RFC1918 / IPv6 ULA)、
// link-local (169.254/169.239、fe80::/10)、cloud metadata (169.254.169.254)、
// IPv4 multicast (224/4)、IPv6 multicast (ff00::/8)、未指定/广播地址.
func blockReasonForIP(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if ip.IsUnspecified() {
		return "host is unspecified address (0.0.0.0 / ::)"
	}
	if ip.IsLoopback() {
		return "host is loopback (127.0.0.0/8 or ::1)"
	}
	if ip.IsLinkLocalUnicast() {
		// 169.254.0.0/16 (IPv4) 与 fe80::/10 (IPv6); 包含 cloud metadata 169.254.169.254
		return "host is link-local (incl. cloud metadata 169.254.169.254)"
	}
	if ip.IsLinkLocalMulticast() {
		return "host is link-local multicast"
	}
	if ip.IsPrivate() {
		return "host is private (RFC1918 or IPv6 ULA fc00::/7)"
	}
	if ip.IsMulticast() {
		return "host is multicast"
	}
	if ip.Equal(net.IPv4bcast) {
		return "host is broadcast (255.255.255.255)"
	}
	return ""
}

// joinBaseAndEndpoint 安全拼接 base 与 endpoint; endpoint 必须已经通过 isValidModelsEndpoint.
//
// base 的 path 段保留 (例 https://a.com/v1 + /models → https://a.com/v1/models).
// url.ResolveReference 把绝对 path 的 reference 视作替换而非追加, 因此改用
// 直接拼接 base.Path 与 endpoint, 再序列化整个 URL. endpoint 不以 "/" 开头则补 "/".
func joinBaseAndEndpoint(base, endpoint string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	if endpoint == "" {
		return baseURL.String(), nil
	}
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	ep, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid models_endpoint: %w", err)
	}
	if ep.Scheme != "" || ep.Host != "" || ep.User != nil {
		return "", fmt.Errorf("models_endpoint must be relative")
	}
	// 拼接: base.Path (含前缀 /) + ep.Path (已确保以 / 开头), 合并 query
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + ep.Path
	if ep.RawQuery != "" {
		baseURL.RawQuery = ep.RawQuery
	}
	return baseURL.String(), nil
}
