package token

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// SignHS256 用 HS256 + 指定 issuer 的 secret 签发 JWT。
//
// 返回 compact 字符串，可直接放入 Authorization: Bearer <token>。
// ttl 必须 > 0；签发后会自动写入 iat / exp / (可选 nbf)。
func SignHS256(issuer Issuer, claims *Claims, ttl time.Duration) (string, error) {
	if issuer.Name == "" {
		return "", errors.New("identity-go: issuer name required")
	}
	if len(issuer.Secret) < 32 {
		return "", errors.New("identity-go: issuer secret too short")
	}
	if claims == nil {
		return "", errors.New("identity-go: claims nil")
	}
	if claims.Subject == "" {
		return "", errors.New("identity-go: claims.Subject required")
	}
	if ttl <= 0 {
		return "", errors.New("identity-go: ttl must be > 0")
	}

	now := time.Now()
	claims.Issuer = issuer.Name
	claims.IssuedAt = now.Unix()
	claims.ExpiresAt = now.Add(ttl).Unix()

	// 使用 RegisteredClaims + 我们自定义 Claims 的混合
	audiences := make(jwt.ClaimStrings, 0, len(claims.AudienceStrings()))
	for _, audience := range claims.AudienceStrings() {
		audiences = append(audiences, audience)
	}
	if len(audiences) == 0 {
		return "", errors.New("identity-go: claims.Audience required")
	}
	registered := jwt.RegisteredClaims{
		Issuer:    issuer.Name,
		Subject:   claims.Subject,
		Audience:  audiences,
		IssuedAt:  jwt.NewNumericDate(time.Unix(claims.IssuedAt, 0)),
		ExpiresAt: jwt.NewNumericDate(time.Unix(claims.ExpiresAt, 0)),
	}
	if claims.NotBefore != 0 {
		registered.NotBefore = jwt.NewNumericDate(time.Unix(claims.NotBefore, 0))
	}

	// 把 Claims 序列化进 MapClaims，Extra 透传
	mc := jwt.MapClaims{}
	mc["iss"] = registered.Issuer
	mc["sub"] = registered.Subject
	if len(registered.Audience) == 1 {
		mc["aud"] = registered.Audience[0]
	} else if len(registered.Audience) > 1 {
		mc["aud"] = registered.Audience
	}
	mc["iat"] = registered.IssuedAt.Unix()
	mc["exp"] = registered.ExpiresAt.Unix()
	if registered.NotBefore != nil {
		mc["nbf"] = registered.NotBefore.Unix()
	}
	if claims.UserID != "" {
		mc["user_id"] = claims.UserID
	}
	if claims.TenantID != "" {
		mc["tenant_id"] = claims.TenantID
	}
	if len(claims.Roles) > 0 {
		mc["roles"] = claims.Roles
	}
	if claims.Scope != "" {
		mc["scope"] = claims.Scope
	}
	for k, v := range claims.Extra {
		if _, exists := mc[k]; !exists {
			mc[k] = v
		}
	}

	t := jwt.NewWithClaims(jwt.SigningMethodHS256, mc)
	return t.SignedString(issuer.Secret)
}
