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

## 7. 后续执行记录（2026-08-18 下午，handoff 后第二会话）

对 §6 遗留工作的逐项执行结果：

| §6 条目 | 状态 | 结果 |
|---|---|---|
| 1. 245 灰度扫描 | ✅ 完成 | 245 无 Go 工具链，改本地交叉编译 `check-credentials`（linux/amd64 静态）后 scp 执行。共享库（252 PG17，245/154 共用）**37 条 credential 全部 `v1-legacy-fernet`，0 解密失败**；id=17 (130dao) 确认 194 字节 envelope、`decrypt_ok=true` |
| 2. VALIDATE CONSTRAINT | ✅ 完成 | **发现：约束此前从未应用到生产库**（migration 079 只进了仓库，`pg_constraint` 查无此约束）。本次在共享库应用 079（NOT VALID，`lock_timeout=10s`）后立即 VALIDATE，`convalidated=t`。245/154 共库，一处生效两边同时受 INSERT/UPDATE 防护 |
| 3. EncryptFernet → AESGCM 迁移 | ⏳ 未动 | 长期项，见 §6 |
| 4. 监控 counter | ✅ 代码完成，待部署 | `llmgw_credential_reveal_failure_total{provider_id,reason}`（详见 §8） |

附带修复与清理：

- **CLI bug**：`cmd/check-credentials/main.go` 的 `encode(updated_at,'escape')` 在
  `updated_at TIMESTAMPTZ`（真实 schema）下报 `function encode(timestamp with time zone, unknown)
  does not exist`——上一会话只跑了单测未连真库所以未暴露。已改为 `updated_at::text`，
  并在 245 真库上验证通过。
- **154 /tmp 残留清理**：删除 `/tmp/testdec/main.go`、`/tmp/enc17.go`（调试用解密 helper，
  含密钥材料暴露面）。
- **154 复发检查**：修复重启（13:03）后最近 15 分钟 `cannot decrypt` 为 0；13:01 前旧进程
  的 8 条 WARN 均为修复前残留。
- 全表 VALIDATE 前置预检查：`credentials` / `credential_keys` 的 bytea ciphertext 列
  首字节 `0x80` 行数均为 0；其余 ciphertext 列（`api_keys.key_ciphertext` 等）为 text
  类型，不受 raw-Fernet-binary 失败模式影响。

## 8. 审计加固（2026-08-18 下午，第三会话审计）

本次审计发现 §6/#1（CLI）和 §6/#4（counter）落地代码存在两处隐患，对应修订：

### 8.1 check-credentials CLI

| 隐患 | 修复 |
|---|---|
| NULL 密文 → `octet_length` / `encode` 返回 NULL → 扫描 `*int` / `*string` 失败，整次 scan abort | `COALESCE(octet_length(...), 0)` + `COALESCE(encode(...), '')`；NULL 行也能继续走 classify |
| `Row.ID/ProviderID` 用 `int`，但 schema 是 `bigint` | 改 `int64`；`flag.Int64` 对齐 |
| `FormatUnknown` 与 `FormatEmpty` 不计入异常、退出码为 0，导致 unknown/empty 被错误地报为 healthy | 两者纳入 `FormatAnom` 计数；非零退出码；`printHuman` 也显式列出 |
| `fix` 用 `WHERE id=$id` 单行 UPDATE，无原值校验，并发轮转时可能覆盖新 ciphertext | 加乐观锁 `WHERE id=$1 AND secret_ciphertext=$2::bytea`；逐行事务；检查 `RowsAffected==1`；并发覆盖记 `RewriteFail`，不视作成功 |

### 8.2 credential reveal 失败 metric

| 隐患 | 修复 |
|---|---|
| 名称叫 `..._decrypt_failure_total` 但囊括了所有 `RevealAPIKey` 错误（含 DB 查询、配置缺失、rotation 失效），把 DB 故障误报为"解密失败" | 重命名为 `llmgw_credential_reveal_failure_total`，反映"获取密钥的全链路失败" |
| 错误分类用 `strings.Contains` 匹配 `secret.DecryptAny` 的错误文案，未来文案 refactor 会静默降级到 `other` | 在 `provider/client.go` 引入 `errReveal*` 哨兵（`errRevealUnknownFormat` / `errRevealDecrypt` / `errRevealNotFound` / `errRevealNotConfigured` / `errRevealRotation` / `errRevealCached`），metric 端用 `errors.Is` 分类 |
| 负缓存只存 `errMsg` 字符串，原始 cause 在缓存丢失，cached 命中时只能报 `cached` 桶、不能保住 `unknown_format` 桶 | `negativeCacheEntry` 增加 `reason` 字段；缓存写入时一次性算好 reason；cached 命中时同时 Inc `cached` + 原 cause 两桶，让 amplify 倍数与原 cause 频率都对 |
| `enrichWithAPIKeys` 处的埋点会被负缓存命中重复计数（每次 reveal 都 Inc 一次） | 缓存命中改在 `RevealAPIKey` 缓存分支埋点；`enrichWithAPIKeys` 用 `errors.Is(err, errRevealCached)` 排除 cached，仅记录 fresh 失败 |

reason 词表收窄：`unknown_format` / `decrypt_error` / `cached` / `not_found` /
`not_configured` / `rotation` / `other`。在 245 实库上重跑 CLI 确认行为不变（id=17
仍 `OK` + sha256 前缀 `08ee3f1b0cb6`）。
