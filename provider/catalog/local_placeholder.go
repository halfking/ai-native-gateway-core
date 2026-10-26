// Package catalog — local_placeholder.go
//
// LocalNoKeyPlaceholder 是本地托管供应商（kind='local'）凭据的占位密钥值。
// 本地服务不校验 API key；凭据行只为满足「路由/discovery/探活需要
// credentials 行」的结构性要求而存在。
//
// 2026-10-26 多模态标准接口轮：该常量从 admin/local_provider.go 提升到本包。
// 历史 bug：启动期 catalog 播种（ensureLocalCredentialsForSeededProviders）
// 曾把占位值**明文**写进 credentials.secret_ciphertext（当时无加密设施），
// 而 admin 与 provider 两面的解密缝都按密文解 ⇒ 播种出来的本地供应商
// 「刷新失败 + 路由 0 候选」双盲。播种路径已改走真加密；两处解密缝
// （admin decryptCred / provider fetchReveal）对**精确等于本常量**的明文
// 直通，兜住全部存量行。占位值是全仓公开常量，不构成秘密；容忍它的
// 明文形态不引入可伪造面（密文列出现该值 ⇔ 播种路径写过它）。
package catalog

// LocalNoKeyPlaceholder is the plaintext placeholder key stored for local
// providers. Exact-match tolerance in the decrypt seams is safe and by-design.
const LocalNoKeyPlaceholder = "local-no-key"
