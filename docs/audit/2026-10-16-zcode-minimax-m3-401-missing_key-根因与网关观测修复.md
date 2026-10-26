# zcode/ZCode 请求 minimax-m3 报 401 missing_key —— 根因与网关观测修复（2026-10-16）

## 一、报障原文

```
Turn execution failed
provider=new-provider-2 provider_code=missing_key model=minimax-m3
request=4def4304-b6a6-4cfc-b009-4c13ae358bec reason=auth_failed status=401 retryable=false
```

`provider_code=missing_key` 是**网关回的**（客户端只是转述），所以第一反应是"key 不对"。
**那个判断是错的**——见第三节。

## 二、事实链（三方日志逐秒对齐）

事发 2026-10-16 17:28–17:29，两台客户端（39.182.10.92 / 111.0.232.150，ZCode 3.15.1）。

| 时刻 | 来源 | 事实 |
|---|---|---|
| 17:28:57 | 154 `/home/vcuser/logs/nginx/access.log` | `POST /v1/responses HTTP/1.1` → **301** 169B，UA `ZCode/3.15.1 ai-sdk/provider-utils/4.0.27 runtime/node.js/24` |
| 17:28:58 | 154 `/var/log/nginx/llm-kxpms-cn-access.log`（443 vhost 独立日志） | `GET /v1/responses HTTP/1.1` → **401** 117B，**同一 UA** |
| 17:28:58 | 客户端 `~/.zcode/cli/log/zcode-2026-10-16.jsonl` | `baseURL: "http://llm.kxpms.cn/v1"`、`statusCode: 401`、`statusMessage: "Missing or malformed Authorization header"`、`x-request-id: 19d7d64a7dae11aaf8528734408f364e` |

两个 301 之后**没有**跟进请求的假象来自一个测量陷阱：:80 的 301 记在全局
`access.log`，:443 记在 vhost 专属的 `llm-kxpms-cn-access.log`（`llm-kxpms-cn.conf:69`
覆盖了 `access_log`）。只翻前者会得出"客户端没跟进"的错误结论。

网关侧的旁证（同一路径实测，2026-10-16 17:38 于 154）：

```
POST https://llm.kxpms.cn/v1/responses（不带 Authorization）
→ 401 117B {"error":{"code":"missing_key","message":"Missing or malformed Authorization header","type":"authentication_error"}}
```

117 字节与客户端记的 `content-length: 117` 一致，**逐字节对上**。

## 三、那把 key 是有效的

`api_keys.id=89`，`key_prefix=sk-RZ8dm0z****`，`enabled=t`，`status=active`，
`owner_user=huangxt`，`last_used_at=2026-10-16 17:36:03`（事发后仍在正常使用）。

所以 `missing_key` **不是**"key 无效"，而是"这一跳上压根没有携带凭据"。

## 四、根因

`~/.zcode/v2/provider_config.json` 里 kaixuan provider（`new-provider-2`）的
`api.baseUrl` 配成了 **`http://llm.kxpms.cn/v1`**（明文 http）。

154 / 245 的 :80 vhost 对**所有**路径一律

```nginx
location / { return 301 https://$host$request_uri; }   # llm-kxpms-cn.conf:40
```

于是：客户端用 http 发起 POST → 边缘 301 到 https → 客户端按 fetch 规范跟进，
**跨源（http→https 是不同 origin）时按规范剥掉 `Authorization`，且 301 会把 POST 降级成 GET**
→ 带不出口令的 `GET /v1/responses` 落到 `AuthMiddleware` → `401 missing_key`。

`AuthMiddleware` 排在路由与 method 检查之前，所以即使方法已降级，先响应的仍是鉴权门。

**同一现象有两种外观**（都源于这条 301，只取决于客户端是否跟进）：

- 跟进（fetch/undici、ai-sdk、aiohttp…）→ 401 `missing_key`，看起来像"key 没配"；
- 不跟进 → 直接死在 301，客户端报一个更含糊的网络错误。

154 的 `gateway-canary-8781.log` 里那条网关自己的 request id `19d7d64a…` **命中 0 次**——
因为 `AuthMiddleware` 挂在 `LoggingMiddleware` **外侧**，被它拒掉的请求根本不产生
`http_request` 记录；而 `missing_key` 分支自己又一条日志都不打。
**网关发出了一个在网关日志里查不到的 request id。** 这是本次排障耗时的主因。

## 五、本次落地

### 1. 客户端配置（真正的解封点）

`~/.zcode/v2/provider_config.json`：`http://llm.kxpms.cn/v1` → `https://llm.kxpms.cn/v1`
（备份 `provider_config.json.bak-pre-https-fix-20261016-174613`）。

实测（154 上用同一把 key、同一个模型、zcode 的 `/v1/responses` 协议）：

```
POST https://llm.kxpms.cn/v1/responses  stream=true  → 200，3.2KB SSE（response.created …）
POST https://llm.kxpms.cn/v1/responses  stream=false → 200，真实 minimax-m3 输出
```

⚠️ 另一台客户端（39.182.10.92 那台机器）**同样要改**，否则它继续吃 301。

### 2. 网关观测修复（本次提交）

- `middleware/auth_mw.go`：`missing_key` 分支补 `slog.Warn("auth: missing API key", …)`
  （此前**零日志**），并给两个拒绝分支都带上 `code` 与 `user_agent`
  ——`user_agent` 是把 401 指认回调用方的唯一字段。
- `cmd/gateway/main.go`：`LoggingMiddleware` 从 `AuthMiddleware` 内侧挪到外侧
  （紧随 `RequestIDMiddleware`）。此后鉴权拒绝也会留下 `http_request` 记录，
  客户端拿到的 `X-Request-Id` 在网关日志里可查。必须排在 RequestID **之内**：
  `RequestIDMiddleware` 把生成的 id 写回 `r.Header["X-Request-Id"]`，再往内排会让
  记录里的 `request_id` 退化成客户端原值。
- `cmd/gateway/auth_rejection_logging_test.go`：行为面 + 接线面两层门（房规同
  `mobile_security_headers_test.go`）。

变异验证（3/3 变红在正确断言）：

| 变异 | 变红的门 |
|---|---|
| Logging 挪回 Auth 内侧 | `TestLoggingMiddlewareSitsOutsideAuthAndInsideRequestID` |
| 删掉 missing_key 的 WARN | `TestAuthRejectionIsGreppableInGatewayLogs`（"auth 拒绝日志数 = 0"） |
| auth 日志去掉 user_agent | 同上（"user_agent = \<nil\>"） |

`go build ./...`、`go vet`、`go test ./middleware/`、`go test ./cmd/gateway/` 全绿。

## 六、明确没做的事

- **没动 nginx 的 301**。让 :80 直接代理数据面确实能让 http:// 客户端跑通，但那是把
  bearer token 放到明文上——现行策略是全 fleet 一致的 TLS 强制（154/245 同款，
  仓库留档 `deploy/nginx/active-20261004/llm-154.kxpms-cn.conf:25`）。
  这是安全姿态变更，不该由一次排障顺手改掉。
- **没把 :80 数据面改成 426**。语义上更贴切（426 = "换个协议再来"），但会把今天
  `curl -L` 还能跑通的脚本一起打死，属于净损失。留作后续议题。
- **没动鉴权判定本身**。`missing_key` 的返回是对的，错的是它不可观测。

## 七、顺带发现（未修，另行跟踪）

排障时发现 `_request_logs` 最后一行停在 **2026-10-13 20:18**，`_request_logs_hot` 为 0 行，
即请求日志入库已断约 3 天（审计统计口径会直接失真）。网关侧对应刷屏：

```
WARN telemetry request db persist failed; fallback written  op=insert  error=context deadline exceeded
```

该状态**已被系统自身识别**——多条后台任务以 `reason: s4_stop_write` 显式跳过而非静默出错：

```
INFO credential_recovery: 36h lookback 扫描已跳过（本轮未执行，非「无候选」） reason=s4_stop_write
```

即：不是本次改动引入，也不是新问题，另有会话在跟。本轮只记录，不并线处理。

## 八、可复用教训

1. **"客户端报鉴权失败"先验 key 状态，再验凭据是否真的到过网关**。本次 key 有效，
   真正丢失发生在**边缘一跳**上，网关只是最后一个看见它的组件。
2. **`http_request` 日志记录不到 ≠ 请求没到**。中间件链里排在 Logging 外侧的拒绝
   （auth/cors/prometheus）全部无记录；判断"网关有没有见过这个 request id"之前，
   先确认那个 request id 会不会被写进日志。
3. **同一 vhost 的 80 与 443 可能落在两个日志文件**，翻错文件会得出相反结论。
4. **边缘的 scheme 跳转会让客户端把"协议不匹配"报成"鉴权失败"**：301 跨源 →
   `Authorization` 被规范剥掉 → POST 降级 GET → 鉴权门先于 method 检查响应 →
   `missing_key`。排查此类"凭据凭空消失"时，把客户端的 `baseUrl` scheme 列为第一嫌疑项。
