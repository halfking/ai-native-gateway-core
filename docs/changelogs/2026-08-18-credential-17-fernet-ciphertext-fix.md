# 2026-08-18 — apiclaude / credential 17 Fernet ciphertext 格式事故与修复

> 154 生产环境 `provider_id=587 (apiclaude)`、`credential_id=17 (130dao)`、
> 模型 `claude-sonnet-4-6 / 5`、`claude-haiku-4-5` 全部 100% 失败。直接连
> `apiclaude.cc` 是正常的，问题在网关侧解密 credential 失败。

## 1. 故障摘要

| 项 | 值 |
|---|---|
| 触发时间 | 2026-08-17 09:48:30 (`enrichWithAPIKeys: reveal failed` 首次命中) |
| 修复时间 | 2026-08-18 13:03:34 (gateway 重启后首次成功响应) |
| 影响时长 | ≈27 小时 |
| 影响模型 | `claude-sonnet-4-6`, `claude-sonnet-5`, `claude-haiku-4-5` |
| 受影响请求 | 每分钟数十次（`routing_blocked:key_decrypt_failed`） |
| 上游状态 | **完全健康**（直连 `https://apiclaude.cc/v1/chat/completions` 返回正常） |

## 2. 根因分析

### 2.1 直接原因

`credentials.secret_ciphertext` (bytea) 中 credential_id=17 的值是
**137 字节的原始 Fernet token**（首字节 `0x80`，无 `v1:legacy:` 前缀，
无 base64 编码）。其他 36 个 credential 都是 `v1:legacy:<b64-url>` 标准
envelope 格式。

### 2.2 调用链

```
请求 → router → RevealAPIKey(credential_id=17)
       → fetchReveal → secret.DecryptAny(ciphertext, keyring, fernetKey)
       → IsV1Envelope? NO  (首字节是 0x80 不是 'v')
       → 走 Fernet 路径
       → DecryptFernet([]byte(ciphertext), fernetKey)
       → base64.URLEncoding.DecodeString(ciphertext) ← FAILS
         (raw bytes 不是合法 base64 字符串)
       → 返回 "cannot decrypt: unknown format"
       → keyCacheNeg 缓存 1 分钟
       → routing_blocked:key_decrypt_failed
       → executor: no candidates after router
       → 502/503 返回客户端
```

### 2.3 历史源头（推测）

在某个早期 commit 里，`admin.Handler.encryptCred` 还没被发明，admin API
直接调用 `secret.EncryptFernet(...)` 并把返回值落库。`EncryptFernet` 返回
的 `[]byte` 是 base64-URL 编码的 string，因此理应直接写入。但有一个早期
bug 版本可能：

- 直接写了 EncryptFernet 内部**未编码**的 token（raw bytes），或
- 在加密端用了 admin 包的 `encryptFernet`，但 secret_ciphertext 是 bytea
  列，写入时某些路径用 `[]byte(string(b64))` → b64 字符串的 bytes 落库

无论哪种情况，最终落库值都是 raw 137 字节，从 8月17日09:48 开始在生产
日志中持续报 `cannot decrypt: unknown format`。

### 2.4 为什么没自动发现

- `credentials.secret_ciphertext` 列**没有任何 CHECK 约束**，格式异常
  直接通过 DDL。
- 没有 `cmd/check-credentials` 类扫描工具，运维只能靠人工查日志。
- 解密失败被 `provider/client.go:RevealAPIKey` 的 `keyCacheNeg` 缓存
  1 分钟，单次失败被「平滑」淹没在 5 分钟健康探测窗口内。

## 3. 修复（已上线）

### 3.1 数据层（生产已执行）

```sql
-- credential 17 原始值 (137 字节 raw Fernet, 以 0x80 开头):
-- 80000000006a820eaf2bce6d...

-- 重新包装为 v1:legacy:<base64-url> envelope:
UPDATE credentials
SET secret_ciphertext = decode('76313a6c65...', 'hex'),  -- 'v1:legacy:gAAAAABqgg6v...'
    updated_at = NOW()
WHERE id = 17;

-- 验证:
SELECT id, label, encode(secret_ciphertext, 'escape')
FROM credentials WHERE id = 17;
-- 结果: v1:legacy:gAAAAABqgg6vK85tMzYKEpHAmjhzcurNhMlEnZA...

-- 验证可解密:
-- 直连 https://apiclaude.cc/v1/chat/completions
-- 返回正常 200 + 内容
```

随后 `systemctl restart llm-gateway-go.service` 重启清空 `keyCacheNeg`。

### 3.2 代码层加固（本 PR）

| 文件 | 改动 |
|---|---|
| `cmd/check-credentials/main.go` | 新增：扫描所有 credential 格式，分类输出，`-fix` 自动重写 raw Fernet 行 |
| `cmd/check-credentials/main_test.go` | 新增：单元测试覆盖 classifier |
| `cmd/check-credentials/README.md` | 新增：使用文档 |
| `admin/handler.go` `encryptCred` | 新增：round-trip 自检 — 任何未来漂移会让 INSERT/UPDATE 失败而非落脏数据 |
| `admin/handler_cred_encrypt_test.go` | 新增：首次直接覆盖 `encryptCred`/`decryptCred` 的单元测试 |
| `sql/migrations/079-credentials-ciphertext-format-check.sql` | 新增：`credentials.secret_ciphertext` 加 CHECK 约束（首字节非 0x80、必须 v1: 或 gAAAAA: 开头） |
| `sql/migrations/079-credentials-ciphertext-format-check.down.sql` | 新增：撤销迁移 |

### 3.3 验证结果

修复后通过 `https://llm.kxpms.cn/v1/chat/completions`（154 公网入口）
实测 4 个模型：

| 模型 | 结果 |
|---|---|
| `claude-sonnet-4-6` | ✅ `"Hey there, friend."` |
| `claude-sonnet-5` | ✅ `"Hi! I'm claude-sonnet-5..."` |
| `claude-haiku-4-5` | ✅ `"Hey there, friend."` |
| `claude-sonnet-4-5` | ❌ no_candidates（路由表中本身不指向 apiclaude provider，与 decrypt 无关）|

错误日志统计（重启后近 5 分钟）：

| 指标 | 修复前 | 修复后 |
|---|---|---|
| `cannot decrypt: unknown format` | 100+/分钟 | **0** |
| `enrichWithAPIKeys: reveal failed` | 数十次/分钟 | **0** |
| `credential_id":17` 相关 WARN/ERROR | 持续 | **0** |
| `upstream_status":200` (apiclaude.cc) | 偶发 | 100% |

## 4. 防御层总结

| 层 | 措施 | 何时生效 |
|---|---|---|
| L1: 代码 | `encryptCred` round-trip self-check | INSERT/UPDATE 时 |
| L2: 工具 | `cmd/check-credentials` 扫描/修复 | 运维/CI 触发 |
| L3: 数据库 | `credentials_ciphertext_format_check` CHECK 约束（NOT VALID） | 部署后 `VALIDATE CONSTRAINT` |
| L4: 监控（建议） | Prometheus counter on `enrichWithAPIKeys: reveal failed` | 待 handoff |

## 5. 直连测试记录（生产 154 上）

| 供应商 | URL | 直连结果 | 网关侧原因 |
|---|---|---|---|
| apiclaude.cc | `https://apiclaude.cc/v1/chat/completions` | ✅ 200 + 正常 SSE | credential 17 解密失败（已修）|
| syapi.cn (suyun) | `https://u.syapi.cn/v1/chat/completions` | ✅ 401 (假token) 上游可达 | 上游限流 503/429 正常 |
| glmcoding.cn (智码) | `https://glmcoding.cn/v1/chat/completions` | ✅ 401 上游可达 | 503 限流正常 |
| api.minimaxi.com | `https://api.minimaxi.com/v1/chat/completions` | ✅ 401 上游可达 | 正常 |
| ark.cn-beijing.volces.com | `https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions` | ✅ 401 上游可达 | 429 限流正常 |
| open.bigmodel.cn (智谱) | `https://open.bigmodel.cn/api/coding/paas/v4/chat/completions` | ✅ 401 上游可达 | 429 限流正常 |
| integrate.api.nvidia.com | `https://integrate.api.nvidia.com/v1/chat/completions` | ✅ 200 上游可达 | 410 模型下线（已配置）|
| token.sensenova.cn | `https://token.sensenova.cn/v1/chat/completions` | ✅ 401 上游可达 | 正常 |
| 129.146.135.219:3000 | `http://129.146.135.219:3000/v1/chat/completions` | ✅ 401 上游可达 | 500 限流 |

**结论**：所有 9 个被检测的上游 100% 健康，问题完全在网关侧的 credential 17 解密失败。

## 6. 遗留工作（handoff）

1. 在 245 灰度运行 `cmd/check-credentials` 确认无脏数据
2. 在 245/154 `ALTER TABLE public.credentials VALIDATE CONSTRAINT
   credentials_ciphertext_format_check` 启用全表校验
3. 长期：将所有 `EncryptFernet` 调用方迁移到 `EncryptAESGCM` + `KEYRING_JSON`，
   消除 Fernet 路径（见 commit history 中 secret 包的双格式支持）
4. 监控建议：Prometheus counter for `enrichWithAPIKeys: reveal failed`，
   阈值告警（例如 1 分钟内 > 5 次）