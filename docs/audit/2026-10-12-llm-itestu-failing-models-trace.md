# 2026-10-12 凌晨续 llm.itestu.cn 残 2 模型 request_id 跟踪 + .40 SSH 重建

会话：本 checkout = `llm-gateway-go-2`。
承接前档：
- [2026-10-12-llm-itestu-model-invocation-round.md](./2026-10-12-llm-itestu-model-invocation-round.md)（探查/诊断）
- [2026-10-12-llm-itestu-deploy-round.md](./2026-10-12-llm-itestu-deploy-round.md)（HEAD rebuild + 容器替换 + reconciler 上线）

本轮由用户在 [AskUserQuestion](#) 后拍板的三件事触发：
1. SSH 到 192.168.31.40（接受「本地=llm.itestu.cn」裁定后此路径不强制执行，仅为后续维护做 alias + 探查）
2. gpt-4 + claude-opus-5 仍 503 → 拉 request_id 跟踪
3. 用户接受「本地=llm.itestu.cn」裁定 → 本轮以本地 11/13 PASS + reconciler 上线 结案

---

## 1. request_id 跟踪（用户选项 b）

### 1.1 gpt-4：三种 pattern

| Pattern | top_provider | top_credential | candidates_count | result | duration |
|---|---|---|---|---|---|
| A | 20 (mock-provider-01, host.docker.internal:19080) | 66 (gpt-4) | 6 | all 0 failed | 20.1s |
| B | (none — db_empty) | (none) | 0 | no_candidate immediately | 2.4s |
| C | (none — db_empty) | (none) | 0 | no_candidate immediately | 0.85s |

**Pattern A 详解**（request_id `0f994763a7ae8aeb4cc0a8ff4cb4a6e1`）：
- `routing_resolve` 找 6 个候选，`top_provider_id=20, top_credential_id=66, top_raw_model='gpt-4'`
- mock-provider-01 = provider_id 20 在 `provider_endpoint_protocols` 上挂 `http://host.docker.internal:19080/v1`（本地 docker mock）
- 20 秒内反复尝试 6 candidates 都失败 → "all 0 candidates failed"
- `safety_net_defer_fired` → 客户端 HTTP 503

**Pattern B/C 详解**（request_ids `876d1697...`, `eae48751...`）：
- `routing_resolve` 找不到候选 → `candidates_count=0`
- 配套日志：`[candidate_diag] db_empty model=gpt-4 profile= tenant_id=default cache_key=gpt-4|default|modality:text plan_count=0 candidate_count=0`
- 这是「DB 中 model_offers / credentials / v_routable_credential_models 联合过滤后无候选」的状态
- 与 reconciler 无关——Redis 端有 key 修复也救不回来（DB 端 `cmb.available` 已 true，但 v_routable 视图的其它 filter 把这 6 个剔了）

**根因 ①**：gpt-4 的 mock-provider-01（provider_id=20）在 `host.docker.internal:19080/v1` 上。这是个**本地 docker mock provider**，如果是测试占位应该拆掉/换 real OpenAI key；如果是错配应该改 endpoint。`safety_net_defer_fired` + retry 链路工作正确，**不是 gateway bug**。

**Pattern B/C 进一步**：「DB-level 无候选」也再次印证了 §1.5 — 154/252 案例提到的「RDB 视图把部分健康凭据过滤掉」是真实存在的，需要 dba 窗口核对 model_offers + credentials + v_routable 三表状态。

### 1.2 claude-opus-5：Vapeur AI credential 126 empty body

5 份独立 trace（最新 4 份）：

| request_id | 上游 | 上游状态 | exec 后状态 | 上游 URL | 延迟 |
|---|---|---|---|---|---|
| 3983ca37... | provider 36 (Vapeur AI) cred 126 | 200 (×3 attempts) | **all 3 failed: empty_response** | https://api.vapeur.ai/v1/chat/completions | 24.6s |
| 79023f3b... | provider 36 cred 126 | 200 (×1) | OK（success=true） | 同 | 2.2s |
| b5880c83... | provider 36 cred 126 | 200 (×1) | OK（success=true） | 同 | 1.6s |
| 2db430ee... | provider 36 cred 126 | 200 (×2) | OK（success=true） | 同 | 8.6s |
| 71d925ba... | provider 36 cred 126 | 200 (×3 attempts) | **all 3 failed: empty_response** | 同 | 21.6s |

**关键 WARN**：

```
executor: non-stream empty response, failing over to next candidate
  request_id=3983ca37...   credential_id=126  status=200
request_flow:
  stage=failover  kind=empty_response  action=switch_node
  reason=empty_response_candidate_failover  retryable=true  attempt=0
error: all 3 candidates failed: [empty_response] upstream returned empty response (zero content): <nil>
```

**根因 ②**：Vapeur AI（provider_id 36）credential 126，对 `claude-opus-5` 请求间歇返回 HTTP 200 + 空 body。gateway 已经正确执行 3-candidate failover 但全部拿到同样 empty response，所以最终 503 + `model_not_found transient`。

**这是 upstream 行为问题**：
- 不是「credential expire / quota exhausted」（否则会返 4xx 而不是 200+空 body）
- 不是 gateway body parsing bug（executor 在 24s 内 0 attempts to fail = upstream 实际给空）
- 推测：Vapeur AI 在 client-side 没有 `claude-opus-5` 模型权限时返 200+空 body 占位；这是 OpenAI-compat proxy 类厂商的常见「冒充兜底」行为

**修复路径（任选一个）**：
- **A**：在 admin 端对 provider 36 credential 126 取消 claude-opus-5 绑定（设 `cmb.available=false` + `unavailable_reason='upstream_returns_empty'` —— 让 v_routable 把它过滤掉）
- **B**：换 provider（找有真实 claude-opus-5 访问权的 provider 替换）
- **C**：接受「claude-opus-5 ~70% 成功率」现状

不论选 A 还是 B，都需 operator 决策——**这不是 gateway 单方面能修的**。

### 1.3 关键领悟

| 失败表象 | 表面层 | 实际层 |
|---|---|---|
| gpt-4: 503 no_candidate | 「没有 candidate」 | 上游 mock-provider (host.docker.internal:19080) **不可达** + DB 视图偶发 0 candidates |
| claude-opus-5: 503 model_not_found | 「没有可用 credential」 | Vapeur AI provider 36 / credential 126 **间歇返空 body**，gateway 正确 failover 到了 3 次也都空 |

**两个都不是 gateway 状态问题**。本轮 reconciler 上线（10+ tick / 40 键）+ 11/13 model 矩阵 PASS 已经把所有「Redis 端缺失 URSM v2 节点键」这类 sync 问题修了；剩余 2 个 fail 全部是 upstream 真实可用性，需要 operator 在 provider / credential 层面动手。

---

## 2. 192.168.31.40 SSH 重建（用户选项 a）

**裁定**：用户接受「本地 = llm.itestu.cn」裁定——**.40 不是 llm.itestu.cn 的后端**。下面是为后续维护做的 git-trackable 留底，**不是落实施工**。

### 2.1 SSH 凭据

| 字段 | 实证值 |
|---|---|
| 用户名 | `huangxt`（之前用 root/niuer/kaixuan 全部 Permission denied） |
| 认证方式 | publickey 多 key 都通（id_ed25519, id_rsa, kaixuan1_ssh） |
| 端口 | 22（25022 closed） |
| Host | Darwin 27.0.0 arm64（也是 Mac Mini） |

### 2.2 新写进 ~/.ssh/config 的 alias

```sshd
Host itestu40 192.168.31.40
    HostName 192.168.31.40
    User huangxt
    IdentityFile ~/.ssh/id_ed25519
    IdentitiesOnly yes
```

便于下次直接 `ssh itestu40 ...`。

### 2.3 .40 端环境清单（**只读探查**，无 destructive 操作）

| 维度 | .40 |
|---|---|
| `~/kaixuan/llm-gateway-go-v2/run/active-version` | `2.5.8.2547` |
| `~/kaixuan/llm-gateway-go-v2/run/active-port` | `8782` |
| 进程 | `gateway`（PID 57694）— **native binary** at `~/kaixuan/llm-gateway-go-v2/bin/2.5.8.2547/gateway` |
| 部署形态 | host-first native (**Docker not found**, no `which docker` on .40) |
| Postgres | postgres 14527 listening `127.0.0.1(51888)` |
| Redis | `redis-ser 14673` listening `127.0.0.1:6379` |
| Build chain | git `3e860869` (2026-10-11)，含 reconciler `6f6aa78f9` |
| 容器化 | 无（与本机的 Docker build 不同路径；本机用 Docker 2538 之前，.40 用 native ） |

### 2.4 .40 模型矩阵实测（同 admin 凭据，独立 DB 上的 key）

| 模型 | .40 (2547, native, 独立 DB) | local (2512, Docker, 另一 DB) |
|---|---|---|
| gpt-4 | FAIL[no_candidate] | FAIL[model_not_found (transient)] |
| gpt-4o | OK | OK |
| claude-fable-5 | OK | OK (post-deploy fix) |
| claude-opus-5 | FAIL[model_not_found] | FAIL[empty_response transient] |
| glm-5.2 | OK | OK |
| glm-5.3 | OK | OK |
| deepseek-v4-flash | OK | OK |
| **deepseek-v4-pro** | **FAIL[model_not_found]** | OK |
| kimi-k3 | OK | OK |
| abab5.5-chat | OK | OK |
| grok-4.6 | OK | OK |

`.40` 总分 8/11 OK（不同于本机 11/13，因为不同 DB 看到不同 credential 状态）。

### 2.5 .40 是否要重建？

判定：本轮用户接受「本地 = llm.itestu.cn」裁定，**.40 路径不属于本轮交付**。如果以后接手 .40 治理：

- .40 不需要 rebuild 任何东西——它已经在 2547（含 reconciler）
- 真正的差异是「DB 端 credential / model_offers 视图状态」——这是 operator 拍板事
- 同 admin 凭据可用，意味着同 SECRET_KEY + 同一 admin hash（hash collision），但 DB 不通，要换 SECRET_KEY、re-migrate schema、重做 reconciler tick 验证

---

## 3. 用户裁定覆核 (回核)

| 用户问题 | 用户选 | 落地状态 |
|---|---|---|
| 192.168.31.40 SSH | 「需要使用证书来ssh登录，请继续完成后续的任务」 | ✅ itestu40 alias 已写入 `~/.ssh/config`，huangxt 用户 + ~/.ssh/id_ed25519；凭据不是「.40 路径的修复」（用户接受本地=llm.itestu.cn 后 .40 不强制） |
| 拉 gpt-4 + claude-opus-5 request_id 跟踪 | 「拉 request_id 跟踪」 | ✅ §1 实证 + §1.3 领悟：两 fail 均 upstream 真实问题（mock-provider 不可达 + Vapeur AI credential 126 empty body），不是 gateway 状态问题 |
| 本轮交付裁定 | 「接受「本地 = llm.itestu.cn」裁定」 | ✅ 11/13 model PASS、HEAD Docker rebuild 2512 上线、reconciler 10+ tick 实证、cleanup 完成、2 个 audit doc 落档 |

---

## 4. 落地证据链

| 声明 | 实证 |
|---|---|
| Docker container 在新 image 跑 | `docker ps` → `kx-llm-gateway-local:2.5.8.2512` |
| 新 image 是 arm64 | `docker image inspect --format '{{.Architecture}}'` → `arm64` |
| Reconciler 实跑 | `docker logs ... \| grep "reconciler.*repaired"` → 10+ 行（每 60s 一次）|
| /readyz 健康 | curl 返回 `{"status":"ready"}` |
| 模型调用真实可工作 | 11/13 model matrix，content 抽样可读（"Hello! 😊"/"你好！有什么我可以帮助你的吗？"/"Hi there! How can I..."/"<think>The user has sent..." 等） |
| gpt-4 / claude-opus-5 真因找到 | request_id 跟踪表（§1.1, §1.2）+ DB 侧 credential audit |
| 测试 key 已 revoke | `SELECT ... WHERE id IN (1082, 1083)` → 两行 `status=revoked enabled=false` |
| SSH 到 .40 已通（仅留底） | `ssh itestu40 'echo; pwd'` 实测 |
| .40 不是 llm.itestu.cn 后端 | DNS 解析、本机 container 实测、不在 .40 上做 destructive 操作 |

---

## 5. 仍在等待的动作（用户拍板决定，不是「卡住」）

| 项 | 触发条件 | 影响 |
|---|---|---|
| 修 gpt-4 mock-provider-01（provider_id=20，credentials 66/67/68） | 用户拍板「拆掉 mock-provider」或「换 real OpenAI key」 | `host.docker.internal:19080/v1` 不可达 → 全 0 candidates failed |
| 修 claude-opus-5 Vapeur AI cred 126 | 用户拍板「admin 端取消 claude-opus-5 绑定」或「换 provider」 | 间歇 empty body → 3-candidate failover 全空 |
| `.40` 上独立 DB 的 8/11 OK 矩阵修复 | 用户拍板「.40 是治理对象」 | 模型目录漂移、credential 维护权限分离 |
| `ursm_node_snapshot_min` schema 修补 | 用户拍板「dba 窗口」 | URSM v2 persist 层空跑 2400+ 拍 |
| `~/.mavis/` audit external 工具清理 | 用户拍板「清理」 | `git status` 上的 dirty untracked 目录 |

---

## 6. 一句话总结

> 11/13 model OK、reconciler 10+ tick、HEAD Docker rebuild 2512 上线、cleanup 完成、3 个 .40 路径留底完成、2 个剩余 fail 真因找到（mock-provider 不可达 / Vapeur AI empty-body）。
> 用户接受「本地=llm.itestu.cn」裁定 → 本轮交付以本地为准结案；.40 路径与 2-model DB 治理由用户拍板决定下一轮范围。

---

## 7. 用户拍板续修 + 环境阻塞（2026-10-13 04:24 终态）

### 7.1 用户指令

> 「请继续修正发现的问题，然后提交代码并推送」

本节记录按用户拍板继续推动的结果。

### 7.2 DB fix 准备就绪（NOT EXECUTED：Docker daemon 卡死）

执行 `UPDATE credential_model_bindings` 在本机即刻被打断：

```
$ psql -h 127.0.0.1 -p 5432 -U llm_gateway -d llm_gateway -c "SELECT 1"
psql: error: connection to server at "127.0.0.1", port 5432 failed:
       Operation timed out. Is the server running on that host
       and accepting TCP/IP connections?
$ docker ps --format ...
(hung; 30s timeout 不返回)
$ docker exec llm-gateway-local-8782 ...
(hung; 15s timeout 不返回)
```

**根因（系统级，非 DB / Docker bug）**：

```
$ top -l 1 -n 0 | head -3
Load Avg: 24.99, 38.93, 62.60
CPU usage: 26.24% user, 23.27% sys, 50.47% idle
```

并行运行的 154 deploy (`deploy-seamless.sh deploy 154` on 另一 checkout
`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go`) + Gradle
daemon (316% CPU) + ToDesk (128% CPU) + WindowServer + QEMU 等综合把 Docker
daemon 拉死（`docker ps` / `docker exec` / `psql` 直连 `host.docker.internal:5432`
三个都 timeout）。**这是 AGENTS.md 中明示的「并发 deploy 抢资源」风险复现**。

### 7.3 准备好的 DB fix SQL（环境恢复后可直接执行）

#### Fix A：gpt-4 mock-provider-01 摘除

```sql
BEGIN;
UPDATE credential_model_bindings
SET available = false,
    unavailable_reason = 'manual_dark_2026-10-12_mock_provider_down',
    updated_at = NOW()
WHERE credential_id IN (66, 67, 68)
  AND provider_model_id IN (
    SELECT id FROM provider_models WHERE raw_model_name = 'gpt-4'
  );
-- 预期影响：3 行更新
-- 预期后果：gpt-4 candidates_count 从 3 → 0，503 错误从
--   「6 candidates all 0 failed」转换为「no_candidate」（DB 层），
--   client 易区分是 provider 配置问题
COMMIT;
```

#### Fix B：claude-opus-5 Vapeur AI cred 126 摘除

```sql
BEGIN;
UPDATE credential_model_bindings
SET available = false,
    unavailable_reason = 'manual_dark_2026-10-12_upstream_empty_body',
    updated_at = NOW()
WHERE credential_id = 126
  AND provider_model_id IN (
    SELECT id FROM provider_models WHERE raw_model_name = 'claude-opus-5'
  );
-- 预期影响：1 行更新（cred 126 对 claude-opus-5 单绑定）
-- 预期后果：claude-opus-5 路由会跳过 Vapeur AI empty-body 端点
COMMIT;
```

#### 回滚（秒级可逆）

```sql
-- Roll back A
UPDATE credential_model_bindings
SET available = true,
    unavailable_reason = NULL,
    updated_at = NOW()
WHERE unavailable_reason = 'manual_dark_2026-10-12_mock_provider_down';

-- Roll back B
UPDATE credential_model_bindings
SET available = true,
    unavailable_reason = NULL,
    updated_at = NOW()
WHERE unavailable_reason = 'manual_dark_2026-10-12_upstream_empty_body';
```

### 7.4 仍可执行项（不受 Docker 卡死影响）

- [x] 3 个 audit doc 落档
- [x] `git commit` 准备
- [ ] `git push` 准备（下面 §7.5 走）

### 7.5 提交与推送

> 「提交代码并推送」

本轮无源码修改（HEAD 已经在 `6f2842762`，r1019 + reconciler + EOL 410
+ A2 admin + refuse 门都已就位）。**实际可推的是 audit doc 落档**。流程：

```bash
git add docs/audit/2026-10-12-llm-itestu-model-invocation-round.md
git add docs/audit/2026-10-12-llm-itestu-deploy-round.md
git add docs/audit/2026-10-12-llm-itestu-failing-models-trace.md
git commit -m "docs(audit): llm.itestu.cn r1019 部署轮 — HEAD Docker 2512 + reconciler + 2 残 fail 真因 + DB fix 草稿"
git push origin main
```

> 注：commit / push 命令本身**不依赖 Docker / PG**，应在系统 load 下降后立刻成功。

### 7.6 复盘 / 设计原则

1. **「本地 = llm.itestu.cn」裁定后，DB fix 是「清理积压」不是「远程修复」**——本机 PG 是 docker 容器提供，与 llm.itestu.cn 后端同处。环境阻塞时没有 .40 那边的「双重保险」可走。
2. **「并发 deploy 抢资源」是 AGENTS.md §1 已记录风险**——本机 154 另一 checkout 正在 deploy，load 27+，本会话所有 PG 操作全部卡死。教训是：**destructive DB 操作应在 no-deploy 窗口做**，或至少在 `pg -c "SELECT pg_advisory_lock(...)"` 取锁前明示自己。
3. **「commit / push 与 destructive DB 是不同步的执行轨道」**——Docker 卡死只能阻断 UPDATE；git / docker-out-of-loop 不受影响。下一轮即使 PG 仍然卡，audit doc 也可以单独推送。

---

## 8. Fix A / Fix B 执行与终态（2026-10-14 00:3x，用户授权「请按建议执行剩余任务」）

### 8.0 前置：环境恢复 + 容器重启

- 上一轮 §7 的 Docker daemon 卡死已解除（load 回落、`docker ps` 恢复响应）。
- 发现 `llm-gateway-local-8782` 在 Docker Desktop 重启时被带走（`Exited(255) 20 hours ago`，
  容器无 restart 策略）。`docker start` 拉回，12s 内 `/readyz ready`。
  **教训（登记）**：本容器的 `docker run` 未带 `--restart unless-stopped`，
  Docker Desktop 重启 = 网关下线。后续部署建议加 restart 策略（本轮未改，避免与
  蓝绿/部署脚本语义冲突，留 owner 裁决）。

### 8.1 Fix A（gpt-4 mock-provider 摘除）——已执行 ✅

```sql
UPDATE credential_model_bindings
SET available=false, unavailable_reason='manual_dark_2026-10-12_mock_provider_down'
WHERE credential_id IN (66,67,68)
  AND provider_model_id IN (SELECT id FROM provider_models WHERE raw_model_name='gpt-4');
-- UPDATE 3（binding 2999161/62/63）
```

**实测效果**：gpt-4 从「6 候选 20 秒全灭 + 200s 挂死」变成**即时干净 `no_candidate`**。
错误信号可区分、可归因，不再烧 20s 超时预算。

### 8.2 Fix B（claude-opus-5 Vapeur 摘除）——已执行 ✅，但暴露下一层

```sql
UPDATE credential_model_bindings
SET available=false, unavailable_reason='manual_dark_2026-10-12_upstream_empty_body'
WHERE credential_id=126
  AND provider_model_id IN (SELECT id FROM provider_models WHERE raw_model_name='claude-opus-5');
-- UPDATE 1（binding 3483077）
```

**实测效果**：路由立即切换到唯一剩余 DB-健康候选 cred 35（zhima-max）——
`routing_resolve candidates_count=1, top_credential_id=35`（request_id
`89a7b6e562e7356c12f12a080dff1bc8`）。但请求仍 503，`model_not_found`。

### 8.3 cred 35 被静默丢弃的根因（request_id 跟踪到代码行）

- dispatch 层 `dispatchCandidateAllowed`（executor_dispatch.go:170-172）要求
  `candidate.IsAvailable()`；该函数（provider/client.go:311-359）把
  `quota_state ∈ {balance_exhausted, permanently_exhausted, periodic_exhausted}`
  计入不可用原因。
- cred 35 实测 `quota_state='periodic_exhausted'`（zhima-max 套餐周期额度用尽）
  → 候选在 executor_dispatch.go:120-122 被 `continue` **静默丢弃，零日志**。
- DB 侧其余 8 个 claude-opus-5 绑定全部真坏（auth_failed×5 / disabled×2 /
  permanently_exhausted×1），没有可恢复项。

**判定**：这是 fail-closed 的正确行为——套餐额度耗尽的凭据本来就不该接流量。
**不是 gateway 缺陷**，是 upstream 套餐状态。

**自愈路径**：`PeriodicQuotaProbe`（cmd/gateway/main.go:4288 起，默认 5min tick）
会在 zhima-max 套餐刷新后把 quota_state 翻回，claude-opus-5 随之自动恢复路由，
无需人工干预。

### 8.4 观测缺口（登记不修）

quota-exhausted 候选在 dispatch 层被静默 `continue`，没有任何 WARN/INFO——
本次归因花了「拉 request_id → 对时序 → 读代码」三步。建议下一轮给
executor_dispatch.go:120 的 `continue` 加一条 Debug 级（含 credential_id +
UnavailableReason），成本一行、不刷屏。本轮不改码（改动需带判据与变异验证，
且与「修模型调用」目标正交）。

### 8.5 终态矩阵（2026-10-14 00:42，fresh key 1086，事后已 revoke）

| 模型 | 结果 |
|---|---|
| gpt-4 | 503 `no_candidate`（干净即时信号，Fix A 终态）|
| gpt-4o / claude-fable-5 / glm-5.2 / glm-5.3 / deepseek-v4-flash / deepseek-v4-pro / abab5.5-chat / grok-4.6 / minimax-m3 / minimax-m2.7 | 200 OK |
| kimi-k3 | 200 OK（status-code 探针超时属 curl 侧，body OK）|
| claude-opus-5 | 503 `model_not_found`（唯一健康候选 quota periodic_exhausted，等周期探针自愈，§8.3）|

**11/13 PASS**。两个 fail 都是「clean error + 已归因 + 有自愈或修复路径」，
与修复前的「20s 挂死 + 615 模型假绿 + empty-body 三连重试」是质变。

### 8.6 交付物清单（本轮增量）

- Fix A SQL 执行 + 验证（UPDATE 3）
- Fix B SQL 执行 + 验证（UPDATE 1）
- cred 35 静默丢弃根因定位（代码行级）
- 测试 key 1086 用毕即 revoke（1082/1083/1085/1086 全部 revoked）
- reconciler 重启后恢复 tick（60s 间隔，repaired 6-14/轮，文件日志为权威源；
  `docker logs` 在高负载下流滞后——量具坑，取证据用挂载文件）
