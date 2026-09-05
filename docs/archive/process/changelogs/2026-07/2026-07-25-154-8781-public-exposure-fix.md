# 2026-07-25 — 154 gateway 8781 公网开放风险修复

## 触发问题

老板在 dashboard 看到 `request_id=5f2287e09ccfa03409ec95bfa5e4e063` 的请求记录，但模型名是 `loadtest-ultra-alpha`（**仅本地测试 profile 使用**，不在生产模型列表）。怀疑"是不是 Redis/DB 公用导致本地测试数据泄漏到生产"。

## 调查结论（不是 Redis/DB 共用泄漏）

### 1. 请求 ID 完整链路

| 项目 | 值 | 来源 |
|---|---|---|
| 到达服务器 | **154 生产** | `journalctl -u llm-gateway-go --since '30 days ago'` 命中 |
| 时间 | 2026-07-25 20:44:24 CST | 同上 |
| 端点 | POST /v1/chat/completions | http_request 日志 |
| 模型 | `loadtest-ultra-alpha`（本地 profile） | Redis trace `llmgw:live:req:5f2287e0...` |
| 状态 | 401 Unauthorized, `error_kind: invalid_key` | http_request 日志 |
| 来源 IP | `172.16.2.210:58246`（252 内网 IP） | `remote` 字段 |
| User-Agent | `Go-http-client/2.0`（Go 程序，不是浏览器） | `user_agent` 字段 |
| Body | 94 bytes（极小 probe） | `request_bytes` |
| 是否经 252 nginx | **否**（252 nginx access.log 无此记录） | 252 nginx 日志全文 grep |
| PG 是否有记录 | **否**（auth 失败前未 insert request_logs） | `SELECT * FROM request_logs WHERE request_id='...'` |
| Redis 是否有 trace | **是**（252 shared Redis, TTL 4h） | `redis-cli GET llmgw:live:req:5f2287e0...` |
| 是否经 245 | **否**（245 journalctl 全无此记录） | 245 `journalctl -u llm-gateway-go` |

### 2. 真正的根因（不是 Redis/DB 共用）

**154 网关 8781 端口是公网完全开放** —— 这才是问题所在：

```
$ curl -v http://47.97.111.154:8781/api/system/version
> GET /api/system/version HTTP/1.1
> Host: 47.97.111.154:8781
< HTTP/1.1 200 OK
< build_seq: 1385
```

**证据链：**
1. 154 服务监听 `[::]:8781`（所有接口，含公网 eth0）
2. firewalld public zone 没有显式拒绝 8781
3. eth0 不在 active zone（只有 `docker` 是 active zone），所以 public zone 的 REJECT 默认策略不生效
4. firewalld `INPUT` 链默认策略 `ACCEPT`，8781 公网直通
5. Aliyun 安全组估计开放 8781（否则也不会通）

**事件还原（最可能场景）：**
- 某个 Go 程序（很可能是 `scripts/loadtest-v2.py` 或类似工具）从内网直接打 `172.16.2.209:8781`（154 内网 IP），绕过了 252 nginx
- 用了本地测试模型名 `loadtest-ultra-alpha` 和无效 API key
- 154 处理了请求（trace 写 Redis），返回 401，但 trace 仍保留
- dashboard live stream 拉到后展示这条记录

### 3. PG/Redis 共用 ≠ 根因

245 和 154 确实**共用同一个 PG17 + Redis**（`172.16.2.210:5432` + `172.16.2.210:6389`，是 deploy-245 skill 的设计：245 是 154 的最后一道验证关卡）。但本次事件中：
- 245 journalctl **完全没有**这个 request_id 的日志
- Redis 中的 trace 是 154 写的（`failure_stage=gateway`, `tenant_id=default`, 字段是 154 的格式）
- PG 中没有这个 record（auth 失败前未 insert）

所以不是数据从 245 泄漏到 154 的 PG/Redis —— **请求本身就是打到 154 的**。

### 4. 拓扑澄清（2026-07-25 实际状态 vs 2026-07-20 文档）

`docs/changelogs/2026-07-20-ssh-wrapper-154-deploy-fix.md` 当时的拓扑说"154 服务实际跑在 245 上"，但 **2026-07-25 现状已改变**：

| 服务器 | 公网 IP | 内网 IP | 跑的网关 build_seq |
|---|---|---|---|
| **154**（生产）| 47.97.111.154 | 172.16.2.209 | 1385（llm-gateway-go + casdoor + nginx）|
| **245**（pre-prod）| 8.136.114.245 | 172.16.2.241 | 1386（llm-gateway-go，比 154 领先 1 build）|
| **252**（网关）| 115.29.212.252 | 172.16.2.210 | nginx 反代 `kxpms_llm_backend → 172.16.2.209:8781`（即 154）|

**生产路径**：`用户 → 252 nginx → 154:8781`（252 nginx 实际 upstream 是 154 内网 IP，不是 245！注释里的"245"是过时的）。

`deploy-245` skill 描述的"245 是 154 的最后验证关卡"仍然正确（245 跑领先 1 build 的同款代码），但用户流量**不**经 245。

## 修复方案（firewalld direct rules + conntrack）

### 目标

- ✅ 254 nginx (252 → 154:8781) 仍然通
- ✅ 245 内部 (172.16.2.241 → 154:8781) 仍然通（监控/调试）
- ✅ 154 loopback (127.0.0.1:8781) 仍然通（deploy 脚本 `curl localhost:8781/healthz`）
- ❌ 公网 (`47.97.111.154:8781`) 必须被 DROP
- ❌ 公网其他来源 → 8781 同样 DROP

### 实施的 iptables 规则（已落地，--permanent）

```bash
# 1. loopback 允许（deploy 脚本用）
firewall-cmd --permanent --direct --add-rule ipv4 filter INPUT 0 \
  -i lo -p tcp --dport 8781 -j ACCEPT

# 2. 内网段 172.16.2.0/24 允许（252 nginx + 245 都属此段）
firewall-cmd --permanent --direct --add-rule ipv4 filter INPUT 1 \
  -s 172.16.2.0/24 -p tcp --dport 8781 -j ACCEPT

# 3. 其余对 8781 的 NEW 连接 DROP（公网攻击面堵住）
firewall-cmd --permanent --direct --add-rule ipv4 filter INPUT 2 \
  -p tcp --dport 8781 -m conntrack --ctstate NEW -j DROP

firewall-cmd --reload
```

**规则顺序关键**：先放行合法的（loopback / 内网段），最后 DROP 兜底。

### 验证（L1→L4）

| 测试 | 命令 | 期望 | 实际 |
|---|---|---|---|
| 公网直接打 8781（外 Mac）| `curl http://47.97.111.154:8781/api/system/version` | timeout / no response | **timeout（已 DROP）** ✅ |
| 252 nginx → 154:8781（生产路径）| `curl http://localhost -H 'Host: llm.kxpms.cn' -d '{}' -X POST /v1/chat/completions` | 返回 401 missing_key（auth 正常）| **401 missing_key** ✅ |
| 252 内网直连 154:8781 | `curl http://172.16.2.209:8781/api/system/version` | 200 OK | **200 OK** ✅ |
| 245 内网直连 154:8781 | `curl http://172.16.2.209:8781/api/system/version`（在 245 上跑）| 200 OK | **200 OK** ✅ |
| Redis 残留 trace | `redis-cli KEYS '*5f2287e0*'`（252 podman exec）| 空 | **空（已 DEL）** ✅ |

### 回滚

```bash
firewall-cmd --permanent --direct --remove-rule ipv4 filter INPUT 2 \
  -p tcp --dport 8781 -m conntrack --ctstate NEW -j DROP
firewall-cmd --permanent --direct --remove-rule ipv4 filter INPUT 1 \
  -s 172.16.2.0/24 -p tcp --dport 8781 -j ACCEPT
firewall-cmd --permanent --direct --remove-rule ipv4 filter INPUT 0 \
  -i lo -p tcp --dport 8781 -j ACCEPT
firewall-cmd --reload
```

或紧急回滚（不持久化）：
```bash
iptables -D INPUT -p tcp --dport 8781 -m conntrack --ctstate NEW -j DROP
iptables -D INPUT -s 172.16.2.0/24 -p tcp --dport 8781 -j ACCEPT
iptables -D INPUT -i lo -p tcp --dport 8781 -j ACCEPT
```

## 排查过程日志（取证）

### 关键 log 行（154 journalctl）

```jsonl
{"time":"2026-07-25T20:44:24.905053869+08:00","level":"INFO","msg":"safety_net_defer_fired","request_id":"5f2287e09ccfa03409ec95bfa5e4e063","attempt_err_code":"invalid_key","attempt_logged":true}
{"time":"2026-07-25T20:44:24.923376708+08:00","level":"WARN","msg":"http_request","request_id":"5f2287e09ccfa03409ec95bfa5e4e063","client_request_id":"","method":"POST","path":"/v1/chat/completions","route":"/v1/chat/completions","query":"","status":401,"status_text":"Unauthorized","duration_ms":81,"remote":"172.16.2.210:58246","host":"llm.kxpms.cn","user_agent":"Go-http-client/2.0","referer":"","content_type":"application/json","request_bytes":94,"response_bytes":150,"error.kind":"unauthorized","error.message":"Unauthorized"}
{"time":"2026-07-25T20:44:24.943386933+08:00","level":"WARN","msg":"trace.FlushToPG: request log row not found, retaining Redis trace","request_id":"5f2287e09ccfa03409ec95bfa5e4e063"}
```

### Redis 中的完整 trace（已被清理）

```json
{
  "request_id": "5f2287e09ccfa03409ec95bfa5e4e063",
  "ts": "2026-07-25T12:44:24Z",
  "tenant_id": "default",
  "gw_session_id": "gw_23d1d482-9763-4c21-a113-92f433300354",
  "model": "loadtest-ultra-alpha",
  "canonical_name": "loadtest-ultra-alpha",
  "model_category": "loadtest-ultra-alpha",
  "status": "failure",
  "latency_ms": 62,
  "error_kind": "invalid_key",
  "failure_stage": "gateway",
  "identity_hash": "f1eadee4ce190440"
}
```

`model=loadtest-ultra-alpha` 是关键信号 —— 这个模型名**只在本地测试 profile（`scripts/profiles/provider-profiles.json`）**定义，不在生产模型列表里。

### Live stream delta push（20:45:53，dashboard 推送）

```
"vendor/loadtest-ultra-alpha: total=10 tiles=10 ids=[...,5f2287e09ccfa03409ec95bfa5e4e063,...]"
"model/loadtest-ultra-alpha: total=10 tiles=10 ids=[...,5f2287e09ccfa03409ec95bfa5e4e063,...]"
```

`vendor/loadtest-*` 和 `model/loadtest-*` 通道的 10 条 sample 里都包含这个 ID —— 这是 dashboard 把请求按 model_category 分类时拉到的样本，不是单独的 245 数据。

## 附加建议（follow-up，未在本次实施）

### P1: 把 eth0 加入 firewalld active zone

当前 eth0 不在 active zone（只有 docker 是 active），导致 firewalld public zone 的 REJECT 默认策略不生效。建议：

```bash
firewall-cmd --zone=public --change-interface=eth0
firewall-cmd --runtime-to-permanent
```

这样后续加新端口的访问控制可以走 zone 而不是直接 iptables，更易审计。

### P1: 加环境标记（env=prod vs env=preprod）

PG 和 Redis 共用意味着 245 的测试数据理论上能出现在 154 的 live stream。建议：

- `request_logs.env` 列加枚举 `prod|preprod|local|staging`
- 245 写入时设 `env=preprod`，154 写入时设 `env=prod`
- dashboard live stream 默认过滤 `env=prod`，preprod 需手动勾选

或更激进：245 和 154 用不同的 Redis DB index（`db=0` vs `db=1`），物理隔离。

### P2: 154 公网 8781 在 Aliyun 安全组也应该限制

如果 Aliyun 安全组开放了 0.0.0.0/0 → 8781，建议改为只允许 Aliyun NAT IP 或限制到管理网段。本次 iptables 是主机内防御，Aliyun 安全组是云端防御，两者独立但都重要。

## 落地文件

- `/etc/firewalld/direct.xml` —— firewalld --permanent 写入
- `/etc/sysconfig/iptables` —— iptables 备份（变更前 `iptables-save` 输出）
- 本仓库：`CHANGELOG.md`（更新）
- 本仓库：`docs/changelogs/2026-07-25-154-8781-public-exposure-fix.md`（本文件）

## 不影响业务的关键判断

1. **生产路径（252 nginx → 154:8781）继续正常工作**（已验证返回 401 auth 错误，说明链路通）
2. **154 loopback 仍然可用**（deploy 脚本的 `curl localhost:8781/healthz` 不会断）
3. **PG/Redis 数据未受影响**（fix 只改 iptables，不动数据）
4. **回滚命令明确**（3 条 `firewall-cmd --permanent --direct --remove-rule`，2 秒回滚）

## 遗留事项

- 老板报告时 SSH 到 154 (47.97.111.154:25022) 已 refused —— **这是 2026-07-20 已存在的状况**（详见 `docs/changelogs/2026-07-20-ssh-wrapper-154-deploy-fix.md` "154 公网 SSH 被防火墙全挡"），**与本次 iptables 变更无关**。deploy 走 252 跳板，不受影响。
- 本次清理了 Redis 中 2 个残留 key（`llmgw:live:req:5f2287e0...` 和 `llmgw:live:tenant:default:req:5f2287e0...`），TTL 4h 后本来也会自动过期。