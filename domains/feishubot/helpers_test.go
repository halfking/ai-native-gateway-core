package feishubot

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

// 重写 signWithKey：使用真正的 HMAC-SHA256 实现，与 VerifyLarkSignature 一致。
//
// 飞书签名公式：base64(HMAC(encrypt_key, ts + nonce + body))
func signWithKey(encryptKey, ts, nonce, body string) string {
	mac := hmac.New(sha256.New, []byte(encryptKey))
	mac.Write([]byte(ts))
	mac.Write([]byte(nonce))
	mac.Write([]byte(body))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
