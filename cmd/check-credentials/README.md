# check-credentials

扫描 `credentials.secret_ciphertext` 列并按格式分类，配套诊断 2026-08-18
154 事故中 credential id=17 (apiclaude/130dao) 被存为 137 字节 raw Fernet
binary 而非标准 `v1:legacy:<b64>` envelope 的场景。

## Usage

```bash
# 人类可读的扫描（默认 dry-run）
go run ./cmd/check-credentials

# 输出 JSON 给 CI / 脚本
go run ./cmd/check-credentials -json

# 自动把 raw-binary Fernet 行重新包装为 v1:legacy:<b64> envelope（默认 dry-run）
go run ./cmd/check-credentials -fix

# 真的写回 DB
go run ./cmd/check-credentials -fix -dry-run=false

# 只看一个 credential
go run ./cmd/check-credentials -credential 17

# 只看一个 tenant
go run ./cmd/check-credentials -tenant default
```

需要以下环境变量（gateway 主程序也会读取）：
- `LLM_GATEWAY_DATABASE_URL` — PG 连接串
- `LLM_GATEWAY_SECRET_KEY` 或 `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` — Fernet 密钥派生源
- 可选：`KEYRING_JSON` + `KEYRING_CURRENT_KID`（AES-GCM keyring）

## Format Classification

| 标签 | 含义 | 可被 DecryptAny 解密？ |
|---|---|---|
| `v1-aes-envelope` | `v1:<kid>:<b64>` AES-GCM envelope | ✅ |
| `v1-legacy-fernet` | `v1:legacy:<b64>` Fernet (URL-safe base64) | ✅ |
| `bare-fernet` | `gAAAAA...` 无 envelope 的 Fernet base64 | ✅（走 DecryptFernet fallback）|
| `raw-fernet-binary` | 首个字节 `0x80`，未编码的 Fernet token | ❌ **会导致 decrypt failure** |
| `empty` | NULL / 空 bytea | N/A |
| `unknown` | 不符合以上任何模式 | ❌ |

## Why a Separate CLI?

`admin.Handler.encryptCred` / `decryptCred` 没有单元测试；最初的脏数据是
**在某个未走 `encryptCred` 的代码路径** 里写入的（早期 admin API 直接调
`EncryptFernet` 并把 raw bytes 落库，没有 `v1:legacy:` 前缀，也没有
base64-encode）。一旦脏数据落库，gateway 会持续把所有 `credential_id=17`
的请求判定为 `routing_blocked:key_decrypt_failed`，没有任何运行时报警。

`check-credentials` 是人工 + CI 友好的检测/修复工具，让运维能在第一时间
发现格式异常的 ciphertext，并在不重启 gateway 的前提下把 raw binary
重新包装成标准 envelope。

## 依赖

仅 `config` + `db` + `secret`（不 import `admin`、`routing`、`maas`），
遵守 `.golangci.yml` 中的 `depguard` 规则。