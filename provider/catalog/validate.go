package catalog

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Validate 检查单个 CatalogEntry 的字段合法性。
// 所有规则对齐 provider_catalog.sql 的 CHECK 约束 + README §5 P1 门禁：
// 重复 key、非法 HTTPS、未知 protocol、缺失 pricing/context metadata 都必须失败。
//
// 注意：Validate 只查单行；重复 Code 跨行检查见 ValidateSet。
func Validate(e CatalogEntry) error {
	var errs []error

	if strings.TrimSpace(e.Code) == "" {
		errs = append(errs, errors.New("code is empty"))
	}
	if !contains(Protocols, e.Protocol) {
		errs = append(errs, fmt.Errorf("code %q: unknown protocol %q (want one of %v)", e.Code, e.Protocol, Protocols))
	}
	if !contains(Tiers, e.Tier) {
		errs = append(errs, fmt.Errorf("code %q: unknown tier %q (want one of %v)", e.Code, e.Tier, Tiers))
	}
	if !contains(Kinds, e.Kind) {
		errs = append(errs, fmt.Errorf("code %q: unknown kind %q (want one of %v)", e.Code, e.Kind, Kinds))
	}
	if !contains(Categories, e.Category) {
		errs = append(errs, fmt.Errorf("code %q: unknown category %q (want one of %v)", e.Code, e.Category, Categories))
	}
	if !contains(DiscoveryStrategies, e.DiscoveryStrategy) {
		errs = append(errs, fmt.Errorf("code %q: unknown discovery_strategy %q (want one of %v)", e.Code, e.DiscoveryStrategy, DiscoveryStrategies))
	}
	if strings.TrimSpace(e.BaseURLTemplate) == "" {
		errs = append(errs, fmt.Errorf("code %q: base_url_template is empty", e.Code))
	} else if err := validateBaseURL(e.Code, e.BaseURLTemplate, e.Kind); err != nil {
		errs = append(errs, err)
	}
	if strings.TrimSpace(e.DisplayName) == "" {
		errs = append(errs, fmt.Errorf("code %q: display_name is empty", e.Code))
	}
	if err := validateNoSecret(e.Code, e); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// validateBaseURL 校验 base_url_template 是 http(s)://。
// local runtime（kind=local，如 ollama/lmstudio）允许 http:// 和 localhost，
// cloud provider 必须是 https://（README: 非法 HTTPS URL 必须失败）。
//
// base_url_template 是 URL 模板，含 {resource}/{host}/{port}/{deployment} 等
// 占位符（SOURCE-VERIFIED from 02-seed.sql：azure-openai、llamacpp 等）。
// 校验前先把占位符替换成合法样例值，再 parse。
func validateBaseURL(code, raw, kind string) error {
	// 替换已知占位符为合法样例，便于 url.Parse。
	expanded := raw
	for ph, sample := range map[string]string{
		"{resource}":   "myresource",
		"{deployment}": "mydeploy",
		"{host}":       "127.0.0.1",
		"{port}":       "8080",
	} {
		expanded = strings.ReplaceAll(expanded, ph, sample)
	}
	u, err := url.Parse(expanded)
	if err != nil {
		return fmt.Errorf("code %q: base_url_template %q unparseable: %w", code, raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	if kind == KindLocal {
		if scheme != "http" && scheme != "https" {
			return fmt.Errorf("code %q: local base_url_template must be http(s)://, got %q", code, scheme)
		}
		return nil
	}
	// cloud
	if scheme != "https" {
		return fmt.Errorf("code %q: cloud base_url_template must be https://, got %q", code, scheme)
	}
	if host == "" {
		return fmt.Errorf("code %q: https base_url_template has no host: %q", code, raw)
	}
	return nil
}

// validateNoSecret 扫描所有字符串字段，拒绝含敏感模式的值。
// 这是 README §5 P1 的 secret 防线：catalog 不得承载 OAuth secret / API key。
func validateNoSecret(code string, e CatalogEntry) error {
	candidates := []string{
		e.Code, e.DisplayName, e.DisplayNameEN, e.BaseURLTemplate,
		e.DocsURL, e.HeaderProfileCode, e.VendorName, e.Notes,
	}
	for _, s := range candidates {
		if secretLikely(s) {
			return fmt.Errorf("code %q: field contains likely secret material (sk-/Bearer/api_key/secret/password): %q", code, s)
		}
	}
	// base_url 的 query string 里也常藏 api_key
	if u, err := url.Parse(e.BaseURLTemplate); err == nil {
		for k, v := range u.Query() {
			if secretLikely(k) || secretLikely(strings.Join(v, "")) {
				return fmt.Errorf("code %q: base_url_template query carries likely secret in %q", code, k)
			}
		}
	}
	return nil
}

// secretLikely 用低误报模式匹配常见 secret 前缀/标记。
// 刻意保守：只匹配高置信度模式，避免把正常 provider 名（如 "passwordkeeper"）
// 误判。catalog 字段不应出现这些模式。
func secretLikely(s string) bool {
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	for _, pat := range []string{
		"sk-",            // OpenAI key 前缀
		"bearer ",        // Authorization header
		"api_key=",       // query/字面量
		"apikey=",        // 同上变体
		"secret=",        // oauth secret
		"password=",      // 明文密码
		"client_secret=", // oauth
	} {
		if strings.Contains(low, pat) {
			return true
		}
	}
	return false
}

// ValidateSet 校验一组 entries：先逐行 Validate，再查重复 Code。
func ValidateSet(entries []CatalogEntry) error {
	var errs []error
	seen := make(map[string]int, len(entries))
	for i, e := range entries {
		if err := Validate(e); err != nil {
			errs = append(errs, fmt.Errorf("entry[%d] code=%q: %w", i, e.Code, err))
		}
		if prev, dup := seen[e.Code]; dup {
			errs = append(errs, fmt.Errorf("duplicate catalog code %q at entry[%d] (first at [%d])", e.Code, i, prev))
		}
		seen[e.Code] = i
	}
	return errors.Join(errs...)
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
