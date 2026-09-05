package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/kaixuan/llm-gateway-go/pkg/httputil"
)

var _ Parser = (*MultiFormatParser)(nil)

const (
	// defaultParserTimeout 订阅拉取超时。
	defaultParserTimeout = 20 * time.Second
	// defaultParserUserAgent 多数机场按 UA 决定返回格式，
	// 带 clash 特征的 UA 才会返回 Clash/Mihomo YAML。
	defaultParserUserAgent = "clash.meta/1.18 (llm-gateway-go)"
	// maxSubscriptionBody 订阅正文大小上限，防止异常响应打满内存。
	maxSubscriptionBody = 16 << 20 // 16MB
	// errBodyExcerptLen 报错时携带的正文摘录长度。
	errBodyExcerptLen = 240

	nodeStatusActive = "active"
)

// MultiFormatParser 订阅解析器，按顺序自动识别三种格式：
//
//  1. Clash/Mihomo YAML（顶层存在 proxies: 键）——生产订阅的真实格式；
//  2. 整体 Base64 编码的 URI 列表（std/URL 编码，带或不带 padding）；
//  3. 纯文本 URI 列表，每行一条。
//
// 注意：trojan/vless/vmess/ss 等协议 Go 的 net/http 无法直接拨号，
// 解析后仅作为库存记录保存（Node.Dialable() == false），
// 需要本地 mihomo/xray 网桥暴露成 http/socks5 入口后再录入才能真正使用。
type MultiFormatParser struct {
	client    *http.Client
	userAgent string
}

// NewMultiFormatParser 创建解析器，使用默认 HTTP 客户端（约 20s 超时）。
func NewMultiFormatParser() *MultiFormatParser {
	return NewMultiFormatParserWithClient(nil)
}

// NewMultiFormatParserWithClient 创建解析器并注入自定义 HTTP 客户端（便于测试）。
// client 为 nil 时退回默认客户端。
func NewMultiFormatParserWithClient(client *http.Client) *MultiFormatParser {
	if client == nil {
		client = &http.Client{Timeout: defaultParserTimeout}
	}
	return &MultiFormatParser{
		client:    client,
		userAgent: defaultParserUserAgent,
	}
}

// Close 关闭 HTTP 客户端的空闲连接（审计修复 2026-08-29 问题 9）。
func (p *MultiFormatParser) Close() {
	if p.client != nil {
		p.client.CloseIdleConnections()
	}
}

// Parse 拉取并解析订阅，返回节点列表。
// 仅在正文无法被任何已知格式解释、或解析出 0 个有效节点时返回错误。
func (p *MultiFormatParser) Parse(ctx context.Context, subscribeURL string) ([]*Node, error) {
	body, err := p.fetch(ctx, subscribeURL)
	if err != nil {
		return nil, err
	}
	return parseSubscriptionBody(body)
}

// fetch 拉取订阅正文。
func (p *MultiFormatParser) fetch(ctx context.Context, subscribeURL string) ([]byte, error) {
	raw := strings.TrimSpace(subscribeURL)
	if raw == "" {
		return nil, errors.New("proxy: empty subscribe url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("proxy: invalid subscribe url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("proxy: unsupported subscribe url scheme %q (want http or https)", u.Scheme)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("proxy: build subscription request: %w", err)
	}
	req.Header.Set("User-Agent", p.userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("proxy: fetch subscription: %w", err)
	}
	body, err := httputil.ReadPrefixAndDrain(resp.Body, maxSubscriptionBody+1)
	if err != nil {
		return nil, fmt.Errorf("proxy: read subscription body: %w", err)
	}
	if len(body) > maxSubscriptionBody {
		return nil, fmt.Errorf("proxy: subscription body too large (over %d bytes)", maxSubscriptionBody)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("proxy: subscription returned HTTP %d; body excerpt: %s",
			resp.StatusCode, bodyExcerpt(body, errBodyExcerptLen))
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("proxy: empty subscription body")
	}
	return body, nil
}

// parseSubscriptionBody 按 Clash YAML -> Base64 -> 纯文本 URI 的顺序尝试解析。
func parseSubscriptionBody(body []byte) ([]*Node, error) {
	var attempts []string

	// 1. Clash/Mihomo YAML
	if looksLikeClashYAML(body) {
		nodes, skipped, err := parseClashYAML(body)
		switch {
		case err != nil:
			attempts = append(attempts, "clash yaml: "+err.Error())
		case len(nodes) > 0:
			logParsed("clash-yaml", len(nodes), skipped)
			return nodes, nil
		default:
			attempts = append(attempts, fmt.Sprintf("clash yaml: 0 valid nodes (%d skipped)", skipped))
		}
	} else {
		attempts = append(attempts, `clash yaml: no top-level "proxies:" key`)
	}

	// 2. 整体 Base64 编码的 URI 列表
	if decoded, ok := decodeBase64Body(body); ok {
		nodes, skipped, err := parseURIList(decoded)
		switch {
		case err != nil:
			attempts = append(attempts, "base64 uri list: "+err.Error())
		case len(nodes) > 0:
			logParsed("base64-uri-list", len(nodes), skipped)
			return nodes, nil
		default:
			attempts = append(attempts, fmt.Sprintf("base64 uri list: 0 valid nodes (%d skipped)", skipped))
		}
	} else {
		attempts = append(attempts, "base64 uri list: body is not a base64 blob")
	}

	// 3. 纯文本 URI 列表
	nodes, skipped, err := parseURIList(body)
	switch {
	case err != nil:
		attempts = append(attempts, "plain uri list: "+err.Error())
	case len(nodes) > 0:
		logParsed("plain-uri-list", len(nodes), skipped)
		return nodes, nil
	default:
		attempts = append(attempts, fmt.Sprintf("plain uri list: 0 valid nodes (%d skipped)", skipped))
	}

	return nil, fmt.Errorf("proxy: unrecognized subscription format (%s); body excerpt: %s",
		sanitizeSecrets(strings.Join(attempts, "; ")), bodyExcerpt(body, errBodyExcerptLen))
}

func logParsed(format string, nodes, skipped int) {
	if skipped > 0 {
		slog.Warn("proxy: subscription parsed with skipped entries",
			"format", format, "nodes", nodes, "skipped", skipped)
		return
	}
	slog.Info("proxy: subscription parsed", "format", format, "nodes", nodes)
}

// ---------------------------------------------------------------------------
// Clash / Mihomo YAML
// ---------------------------------------------------------------------------

// clashProxiesKeyRe 匹配顶层 proxies: 键（行首无缩进）。
var clashProxiesKeyRe = regexp.MustCompile(`(?m)^proxies[ \t]*:`)

func looksLikeClashYAML(body []byte) bool {
	return clashProxiesKeyRe.Match(body)
}

// clashDocument 只关心 proxies 段，其余字段（dns/rules/proxy-groups...）忽略。
type clashDocument struct {
	Proxies []map[string]interface{} `yaml:"proxies"`
}

// clashReservedKeys 已映射到 Node 结构化字段的键，不再重复放入 Config。
var clashReservedKeys = map[string]struct{}{
	"name":     {},
	"server":   {},
	"port":     {},
	"type":     {},
	"username": {},
	"password": {},
}

// parseClashYAML 解析 Clash/Mihomo 配置的 proxies 段。
func parseClashYAML(body []byte) (nodes []*Node, skipped int, err error) {
	var doc clashDocument
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, 0, fmt.Errorf("decode yaml failed: %s", sanitizeSecrets(err.Error()))
	}
	if len(doc.Proxies) == 0 {
		return nil, 0, errors.New(`no entries under "proxies:"`)
	}

	nodes = make([]*Node, 0, len(doc.Proxies))
	for _, raw := range doc.Proxies {
		node, ok := nodeFromClashProxy(raw)
		if !ok {
			skipped++
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes, skipped, nil
}

// nodeFromClashProxy 将单个 clash proxy 条目映射为 Node。
// server 为空或端口越界时返回 false（计为 skipped，不影响整体解析）。
func nodeFromClashProxy(raw map[string]interface{}) (*Node, bool) {
	server := strings.TrimSpace(asString(raw["server"]))
	port, portOK := asInt(raw["port"])
	proxyType := strings.ToLower(strings.TrimSpace(asString(raw["type"])))
	if server == "" || !portOK || port < 1 || port > 65535 || proxyType == "" {
		return nil, false
	}

	name := strings.TrimSpace(asString(raw["name"]))
	if name == "" {
		name = net.JoinHostPort(server, strconv.Itoa(port))
	}

	tls, _ := asBool(raw["tls"])

	// 其余协议相关字段（uuid/flow/sni/servername/reality-opts/
	// skip-cert-verify/network/udp/client-fingerprint/dialer-proxy/cipher/alterId...）
	// 全量保留到 Config，避免信息丢失。
	cfg := make(map[string]interface{}, len(raw))
	for k, v := range raw {
		if _, reserved := clashReservedKeys[strings.ToLower(k)]; reserved {
			continue
		}
		cfg[k] = v
	}
	if len(cfg) == 0 {
		cfg = nil
	}

	return &Node{
		Name:     name,
		Protocol: clashProtocol(proxyType, tls),
		Server:   server,
		Port:     port,
		Username: asString(raw["username"]),
		Password: asString(raw["password"]), // 此层为明文，写库前由 Store 加密
		Config:   cfg,
		Location: guessLocation(name),
		Status:   nodeStatusActive,
	}, true
}

// clashProtocol 映射 clash type 到 Node.Protocol。
// http/socks5 可被 Go 直接拨号；ss/vmess/trojan/vless 等保留字面协议名，仅作库存。
func clashProtocol(clashType string, tls bool) string {
	switch clashType {
	case "http":
		if tls {
			return ProtocolHTTPS
		}
		return ProtocolHTTP
	case "https":
		return ProtocolHTTPS
	case "socks5", "socks5h", "socks":
		return ProtocolSOCKS5
	default:
		return clashType
	}
}

// ---------------------------------------------------------------------------
// Base64 正文
// ---------------------------------------------------------------------------

var base64BlobRe = regexp.MustCompile(`^[A-Za-z0-9+/\-_]+={0,2}$`)

// decodeBase64Body 尝试把整个正文当作 base64 解码（std/URL 编码，带或不带 padding）。
func decodeBase64Body(body []byte) ([]byte, bool) {
	compact := stripWhitespace(string(body))
	if len(compact) < 8 || !base64BlobRe.MatchString(compact) {
		return nil, false
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(compact)
		if err != nil || len(decoded) == 0 {
			continue
		}
		// 解码结果必须像 URI 列表，否则认为不是这种格式。
		if bytes.Contains(decoded, []byte("://")) {
			return decoded, true
		}
	}
	return nil, false
}

func stripWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// looksLikeSingleBase64Blob 判断正文是否为一整块不可读的 base64（报错摘录时不外泄内容）。
func looksLikeSingleBase64Blob(s string) bool {
	compact := stripWhitespace(s)
	return len(compact) > 60 && base64BlobRe.MatchString(compact)
}

// ---------------------------------------------------------------------------
// URI 列表
// ---------------------------------------------------------------------------

// parseURIList 解析每行一条 URI 的订阅正文。
func parseURIList(body []byte) (nodes []*Node, skipped int, err error) {
	sawURI := false
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 纯注释行（'#' 开头的不是 URI，URI 的 '#' 只会出现在 scheme 之后）
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") ||
			strings.HasPrefix(line, ";") {
			continue
		}
		if !strings.Contains(line, "://") {
			skipped++
			continue
		}
		sawURI = true
		node, ok := nodeFromURI(line)
		if !ok {
			skipped++
			continue
		}
		nodes = append(nodes, node)
	}
	if !sawURI {
		return nil, skipped, errors.New("no proxy URI lines found")
	}
	return nodes, skipped, nil
}

// uriProtocol 映射 URI scheme 到 Node.Protocol。
func uriProtocol(scheme string) (string, bool) {
	switch scheme {
	case "http":
		return ProtocolHTTP, true
	case "https":
		return ProtocolHTTPS, true
	case "socks5", "socks5h", "socks":
		return ProtocolSOCKS5, true
	case "ss", "ssr", "vmess", "vless", "trojan", "trojan-go", "hysteria", "hysteria2", "tuic":
		// 仅库存记录，Dialable() == false
		return scheme, true
	default:
		return "", false
	}
}

// credentialInUserinfoSchemes 这些协议的 userinfo 是单一凭据（密码或 uuid）。
var credentialInUserinfoSchemes = map[string]bool{
	"ss":        true,
	"ssr":       true,
	"trojan":    true,
	"trojan-go": true,
	"vmess":     true,
	"vless":     true,
	"tuic":      true,
	"hysteria":  true,
	"hysteria2": true,
}

// nodeFromURI 解析单条代理 URI，例如：
//
//	http://user:pass@host:8080#Name
//	socks5://host:1080#Name
//	trojan://password@host:443?sni=a.b#Name
func nodeFromURI(rawURI string) (*Node, bool) {
	u, err := url.Parse(rawURI)
	if err != nil {
		return nil, false
	}
	scheme := strings.ToLower(u.Scheme)
	protocol, ok := uriProtocol(scheme)
	if !ok {
		return nil, false
	}

	extra := make(map[string]interface{})

	// 传统 ss:// 把 method:password@host:port 整体 base64 编码，需要先还原。
	if scheme == "ss" && u.Port() == "" {
		if decoded, ok := decodeSSLegacyHost(u.Host); ok {
			if reparsed, err := url.Parse("ss://" + decoded); err == nil {
				fragment := u.Fragment
				rawQuery := u.RawQuery
				u = reparsed
				if u.Fragment == "" {
					u.Fragment = fragment
				}
				if u.RawQuery == "" {
					u.RawQuery = rawQuery
				}
			}
		}
	}

	server := u.Hostname()
	port, portOK := asInt(u.Port())
	if server == "" || !portOK || port < 1 || port > 65535 {
		return nil, false
	}

	username := u.User.Username()
	password, hasPassword := u.User.Password()
	if !hasPassword && username != "" && credentialInUserinfoSchemes[scheme] {
		// trojan://<password>@host、vless://<uuid>@host
		password, username = username, ""
		switch scheme {
		case "vless", "vmess":
			extra["uuid"] = password
		case "ss":
			// 少见的 ss://method:password 形态已在上面还原，这里只剩单一密码
		}
	}
	if scheme == "ss" && username != "" && hasPassword {
		// ss://method:password@host:port —— username 位其实是加密方式
		extra["cipher"] = username
		username = ""
	}

	for key, values := range u.Query() {
		if len(values) == 1 {
			extra[key] = values[0]
			continue
		}
		extra[key] = values
	}

	name := strings.TrimSpace(u.Fragment)
	if name == "" {
		name = net.JoinHostPort(server, strconv.Itoa(port))
	}

	var cfg map[string]interface{}
	if len(extra) > 0 {
		cfg = extra
	}

	return &Node{
		Name:     name,
		Protocol: protocol,
		Server:   server,
		Port:     port,
		Username: username,
		Password: password, // 明文，写库前加密
		Config:   cfg,
		Location: guessLocation(name),
		Status:   nodeStatusActive,
	}, true
}

// decodeSSLegacyHost 解码传统 ss:// 的 base64 主机段。
func decodeSSLegacyHost(host string) (string, bool) {
	if host == "" || !base64BlobRe.MatchString(host) {
		return "", false
	}
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding,
	} {
		decoded, err := enc.DecodeString(host)
		if err != nil {
			continue
		}
		if s := string(decoded); strings.Contains(s, "@") && strings.Contains(s, ":") {
			return s, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// 地区推断
// ---------------------------------------------------------------------------

// locationRules 从节点名尽力推断地区码。Keywords 为子串匹配（中文等），
// Tokens 为 ASCII 整词匹配（避免 "US" 命中 "Russia" 之类的误判）。
// 顺序敏感：更具体的规则必须在前（如 "印度尼西亚" 必须早于 "印度"）。
var locationRules = []struct {
	Code     string
	Keywords []string
	Tokens   []string
}{
	{"TW", []string{"台湾", "臺灣", "台北"}, []string{"TW", "TAIWAN"}},
	{"HK", []string{"香港"}, []string{"HK", "HONGKONG"}},
	{"MO", []string{"澳门", "澳門"}, []string{"MO", "MACAO", "MACAU"}},
	{"AU", []string{"澳洲", "澳大利亚"}, []string{"AU", "AUSTRALIA"}},
	{"US", []string{"美国", "美國"}, []string{"US", "USA"}},
	{"JP", []string{"日本", "东京", "大阪"}, []string{"JP", "JAPAN"}},
	{"SG", []string{"新加坡", "狮城"}, []string{"SG", "SINGAPORE"}},
	{"KR", []string{"韩国", "韓國", "首尔"}, []string{"KR", "KOREA"}},
	{"GB", []string{"英国", "英國", "伦敦"}, []string{"UK", "GB"}},
	{"ID", []string{"印度尼西亚", "印尼"}, nil}, // 必须早于 IN
	{"IN", []string{"印度"}, nil},
	{"DE", []string{"德国", "德國"}, nil},
	{"FR", []string{"法国", "法國"}, nil},
	{"IT", []string{"意大利"}, nil},
	{"NO", []string{"挪威"}, nil},
	{"CA", []string{"加拿大"}, []string{"CANADA"}},
	{"RU", []string{"俄罗斯", "俄羅斯"}, nil},
	{"UA", []string{"乌克兰", "烏克蘭"}, nil},
	{"AE", []string{"阿联酋", "迪拜"}, nil},
	{"NG", []string{"尼日利亚"}, nil},
	{"TH", []string{"泰国", "泰國"}, nil},
	{"TR", []string{"土耳其"}, nil},
	{"VN", []string{"越南"}, nil},
	{"PH", []string{"菲律宾", "菲律賓"}, nil},
	{"MY", []string{"马来西亚", "馬來西亞"}, nil},
	{"BR", []string{"巴西"}, nil},
	{"AR", []string{"阿根廷"}, nil},
}

// guessLocation 尽力从节点名推断地区码，识别不出时返回空串。
func guessLocation(name string) string {
	if name == "" {
		return ""
	}
	tokens := asciiTokens(name)
	for _, rule := range locationRules {
		for _, kw := range rule.Keywords {
			if strings.Contains(name, kw) {
				return rule.Code
			}
		}
		for _, tok := range rule.Tokens {
			if tokens[tok] {
				return rule.Code
			}
		}
	}
	return ""
}

// asciiTokens 提取名字里的 ASCII 单词（大写）用于整词匹配。
func asciiTokens(name string) map[string]bool {
	fields := strings.FieldsFunc(strings.ToUpper(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	tokens := make(map[string]bool, len(fields))
	for _, f := range fields {
		tokens[f] = true
	}
	return tokens
}

// ---------------------------------------------------------------------------
// 取值辅助
// ---------------------------------------------------------------------------

func asString(v interface{}) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case []byte:
		return string(val)
	case bool:
		return strconv.FormatBool(val)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case uint64:
		return strconv.FormatUint(val, 10)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func asInt(v interface{}) (int, bool) {
	switch val := v.(type) {
	case nil:
		return 0, false
	case int:
		return val, true
	case int32:
		return int(val), true
	case int64:
		return int(val), true
	case uint:
		return int(val), true
	case uint32:
		return int(val), true
	case uint64:
		return int(val), true
	case float64:
		if val != float64(int(val)) {
			return 0, false
		}
		return int(val), true
	case string:
		trimmed := strings.TrimSpace(val)
		if trimmed == "" {
			return 0, false
		}
		n, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func asBool(v interface{}) (bool, bool) {
	switch val := v.(type) {
	case nil:
		return false, false
	case bool:
		return val, true
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(val))
		if err != nil {
			return false, false
		}
		return b, true
	default:
		return false, false
	}
}

// ---------------------------------------------------------------------------
// 报错脱敏
// ---------------------------------------------------------------------------

var (
	// secretKVRe 匹配 key: value / key=value 形式的敏感字段。
	secretKVRe = regexp.MustCompile(`(?i)(password|passwd|pwd|uuid|psk|token|secret|api[-_]?key|public-key|short-id|auth)([ \t]*[:=][ \t]*)("?)([^\s",}]+)`)
	// userinfoRe 匹配 scheme://userinfo@host 中的凭据。
	userinfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/\s@]+@`)
)

// sanitizeSecrets 移除字符串中的凭据，任何进入日志/错误的文本都要先过一遍。
func sanitizeSecrets(s string) string {
	s = secretKVRe.ReplaceAllString(s, "${1}${2}[REDACTED]")
	s = userinfoRe.ReplaceAllString(s, "${1}[REDACTED]@")
	return s
}

// SanitizeSecrets 公开包装，供其他包（如 manager）在打日志前调用。
// 二次审计修复 (2026-08-29)：补强 redactErr，复用同款脱敏逻辑以覆盖
// key=value / password=xxx / token=xxx 等键值对形式。
func SanitizeSecrets(s string) string { return sanitizeSecrets(s) }

// bodyExcerpt 生成用于排障的正文摘录（已脱敏、已截断、单行）。
func bodyExcerpt(body []byte, max int) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return `""`
	}
	if looksLikeSingleBase64Blob(trimmed) {
		// 整块 base64 很可能就是带凭据的 URI 列表，不外泄内容。
		return fmt.Sprintf("<opaque base64-like body, %d bytes>", len(body))
	}
	if max <= 0 {
		max = errBodyExcerptLen
	}
	head := trimmed
	if len(head) > max*4 {
		head = strings.ToValidUTF8(head[:max*4], "")
	}
	head = sanitizeSecrets(head)
	if len(head) > max {
		head = strings.ToValidUTF8(head[:max], "") + "..."
	}
	return strconv.Quote(head)
}
