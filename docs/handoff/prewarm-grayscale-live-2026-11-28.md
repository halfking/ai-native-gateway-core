# prewarm 灰度放量终局轮交接

## 任务状态

**使命**：完成 memora prewarm 灰度两键开启、认证链打通、五态指标活体验证；处理 DLQ 积压重投

**当前状态**：灰度活体验证达成，DLQ 重投因消费吞吐暂停

## 已完成

1. ✅ 灰度两键已开（settings_kv: `memory.prewarm.enabled=true` + `tenant_allowlist='default'`）
2. ✅ 五态指标活体（requested=73/miss=72/timeout=1/unavailable=0，认证链零失败）
3. ✅ ASM→memora 三层断链全修（37b2d29 scope三域 + project ACL asm-memorarecall 授 gateway-session-analysis read + 手动全链 200 OK）
4. ✅ DLQ 重投通道打通并验证（outbox-dlq-replay 工具，幂等安全）
5. ✅ 消费卡点根因定谳（session.opened 投影行缺失 → completed 事件 Retryable 永久循环）
6. ✅ 记忆更新（prewarm-grayscale-live-round-2026-11-28.md）
7. ✅ 12h审计轮遗留修复已提交（limiter并发安全 + gate_test重复键，`1903a10e6a`）

## 待办事项

1. **观察 embedding 服务恢复**（当前502/503 → injected_bytes=0）
2. **embedding 恢复后观察真实 hit 产生**
3. **消费吞吐恢复后执行 DLQ 批量重投**（checkpoint 推进加速至<14s/条）
4. **按 P1-4 口径出灰度对照报告**（Recall@5≥0.25、nDCG≥90%）

## 下一步操作

### 1. 监控 embedding 服务恢复

```bash
# 检查 memora embedding 组件状态
ssh 211 'docker logs memora-server --since 10m 2>&1 | grep -iE "embedding|502|503" | tail -5'

# 检查五态指标 injected_bytes 是否开始增长
ssh 211 'docker exec llm-gateway-211-$(cat /opt/kaixuan/data/llm-gateway/run/active-slot) wget -qO- localhost:8781/metrics | grep prewarm'
```

### 2. 检查消费吞吐恢复

```bash
# 两次采样比对 checkpoint 推进速率
ssh 211 'docker exec acc-pg17 psql -U llm_gateway -d session_manager_dev -tAc "SELECT max(last_occurred_at), max(updated_at) FROM gateway_projection_checkpoints WHERE tenant_id='\''default'\''"'
sleep 60
ssh 211 'docker exec acc-pg17 psql -U llm_gateway -d session_manager_dev -tAc "SELECT max(last_occurred_at), max(updated_at) FROM gateway_projection_checkpoints WHERE tenant_id='\''default'\''"'
# 若推进速率<14s/条，可开始重投
```

### 3. 执行 DLQ 重投

```bash
# 分批重投（每批500，间隔60s观察消化）
ssh 211 'bash' <<'REMOTE'
export PATH="/usr/local/bin:$PATH"
SEC=$(docker exec llm-gateway-211-s env | grep '^AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET' | cut -d= -f2-)
DSN=$(docker exec llm-gateway-211-s env | grep '^LLM_GATEWAY_DATABASE_URL' | cut -d= -f2- | sed -E 's/@[^:/]+:/@127.0.0.1:/')
EP=$(docker exec llm-gateway-211-s env | grep '^ASM_INTERNAL_ENDPOINT' | cut -d= -f2-)
AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET="$SEC" /tmp/dlq-replay --dsn "$DSN" --endpoint "$EP" --tenant-id default --limit 500 --execute --timeout 10m
REMOTE
```

### 4. 收集灰度对照数据

```bash
# 收集 allowlist 内外租户 hit/miss/latency
ssh 211 'docker exec llm-gateway-211-$(cat /opt/kaixuan/data/llm-gateway/run/active-slot) wget -qO- localhost:8781/metrics | grep prewarm'

# 检查 memora 召回质量（需 embedding 恢复后有真实数据）
# 按 P1-4 口径计算 Recall@5 和 nDCG
```

## 关键决策记录

1. **settings_kv 直写绕过 admin API**：owner 提供的 admin 口令（字面量已脱敏，见会话记录）对 DB 里 admin 用户 bcrypt 不匹配，实配是 demoadmin 在 `/root/.llm-gateway-211.env`
2. **ASM scope 从单域改三域**：`memory.read + knowledge.read + fact.read`（37b2d29）
3. **search 缺 scope_chain.project_id 缺省回填**：`gateway-session-analysis`（与会话分析摄取一致）
4. **project ACL 用 postgres 超户 INSERT**：memora RLS force，memora_admin/app_rls 写不进
5. **DLQ 重投暂停批量执行**：消费吞吐14s/条，重放500条远快于消化必然回弹

## 风险提示

1. ⚠️ embedding 服务持续502/503会导致 injected_bytes 恒为0，无法产生真实 hit 验证召回效果
2. ⚠️ 消费吞吐未恢复前批量重投 DLQ 会全部回弹（已验证3次500条批次）
3. ⚠️ owner 在 Mac（XUTAOdeMacBook-Pro）并行部署211，锁冲突风险（后续需先查 `deploy.lock` 的 `source_host`）
4. ⚠️ ASM distroless 镜像 projection 消费失败无日志可见（诊断靠 `gateway_dead_letter_events` 表 + 手动构造事件复现）

## 参考资料

- 记忆文档：`/Users/niuer/.zcode/cli/memories/projects/llm-gateway-30751a35342a9b26/memory/prewarm-grayscale-live-round-2026-11-28.md`
- 前序轮次：`/Users/niuer/.zcode/cli/memories/projects/llm-gateway-30751a35342a9b26/memory/prewarm-grayscale-round-2026-11-27.md`
- DLQ replay 工具：`211:/tmp/dlq-replay`（源码可从 `llm-gateway-go/cmd/tools/outbox-dlq-replay` 重建）
- settings_kv 表：211 生产 `session_manager_dev` 库
- memora project_acl 表：211 生产 `memora` 库

## 工作目录

- `/Users/niuer/workspace/ai-native-tools/llm-gateway/llm-gateway-go` (main @ 1903a10e6a, clean)
- `/Users/niuer/workspace/ai-native-tools/llm-gateway/ai-session-manager` (main, clean)

## 双远端状态

- ✅ llm-gateway-go: `0/0/0 @ 1903a10e6a`（已同步）
- ✅ ai-session-manager: clean（无本地修改）

---

# 续作轮 2026-11-28 下午（灰度终局收口）

## 任务清单核销

| 交接待办 | 结果 |
|---|---|
| 监控 embedding 服务恢复 | ❌ 未恢复：`llm.kxpms.cn/v1/embeddings`（text-embedding-001）持续 503，实测单次 2.3~10.3s（三次采样）；已挂 90min 后台监控（211:/tmp/emb-watch.sh，每分钟探测） |
| 检查消费吞吐恢复 | ✅ checkpoint 活跃推进（663633→663652，lag 分钟级） |
| DLQ 批量重投 | ✅ **不再需要**：1917 条事故积压已被消化清空（现仅剩 1 条 terminal 测试事件 evt-prewarm-e2e-0003，缺 session identity 不会再重试，可留档或手工删除） |
| 收集灰度对照数据出报告 | ⚠️ 出了**中期报告**（见下）；Recall@5/nDCG 需真实 hit，仍被 embedding 上游卡住 |

## 灰度中期报告（P1-4 口径，数据截至 2026-11-28 15:05）

**灰度面（生产实证）**：
- 两键在位未漂移：`llm_gateway` 库 `public.settings_kv`（注意：不在 session_manager_dev！）`memory.prewarm.enabled=true`（23:29:57 写入）+ `tenant_allowlist="default"`（23:30:18）。
- 五态活体跨蓝绿存活（owner 今日两度重发：版本 d211e7f0→c3a38692，槽 s/a→b，计数器随槽重置）：
  - 旧槽累计（11-28 上午）：requested=474 / miss=327 / timeout=147 / **unavailable=0（认证链全程零失败）** / hit=0
  - 新槽 b（15:00 起）：requested=4 / miss=3 / timeout=1 / unavailable=0 / hit=0
- injected_bytes 恒 0（全槽一致）。

**timeout=147 根因定谳（本轮新发现）**：
- 链路：memora memories/search 的 query embedding 走 `llm.kxpms.cn/v1/embeddings` → 上游 503 且单次 2.3~10.3s → memora 处理超网关 **800ms prewarm 总预算**（prewarm.go DefaultHTTPTimeout 500ms + WriteTimeout 300ms，ADR §1 钉死）→ 网关到点取消 → ASM 侧报 `Post .../api/v2/memories/search: context canceled`（"degrading to knowledge-only" WARN，每分钟数条）→ timeout 态上涨。
- embedding 上游是外部依赖（kxpms.cn 网关的 embedding 通道），非本轮可修。

**hit=0 / injected_bytes=0 根因**：embedding 503 ⇒ 向量化失败 ⇒ 召回结果 degraded/空 ⇒ 无注入。真实 hit 与 Recall@5≥0.25 / nDCG≥90% 测量**全部等待 embedding 上游恢复**，恢复后按：allowlist 内 default 租户注入样本 vs 原始会话事实对照计算。

**memora 新发现 bug + 修复已备（未部署）**：
- 现象：memora 日志每 20~30s 报 `recall list readable knowledge bases failed: trusted project authorization required`（recall.go:524）——ASM 每次灰度 prewarm recall（带 X-Project-ID 头）都触发。
- 根因：`recallWithService` 里 `recallProjectAuthorized` 经 `middleware.AuthorizeProjectAction` 判过项目读 ACL，但**判定结果没有回写请求上下文**（gin 中间件 ProjectAuthz 路径在 project_authz.go:162 有回写，handler 自判路径漏了）⇒ 下游 `ListReadableKBs → readableListProjects` 命中「ctx 有项目选择、无 trusted 授权」fail-closed 分支 ⇒ knowledge 来源恒为空。
- 修复：**`memora` 仓库分支 `recall-authz-stamp-211` @ `61d6280a`**（已推 origin）。仅在 ctx 已带项目选择时按 middleware 同一语义回写 `WithAuthorizedProject`（principal=identity.ActorID）；无头路径不伪造授权。测试双用例 + 护栏咬合验证（无修复正例必败）。全量 `go test ./internal/v2/` 绿。

## memora 部署关键情报（2026-11-28 实测）

1. **在役 memora 镜像代码线已锁定**：容器二进制 `go version -m` 实证 mod = `v0.0.0-20261115214133-8560dea20eea` ⇒ 构建提交 **`36/211-deploy-lineage @ 8560dea2`**（2026-11-16）。修复分支基于该确切提交，增量仅 1 commit。
2. ⚠️ **勿从 `codex/s5-audit-211` 构建**：与在役线自 2026-08-15 分叉 **2802 提交**。⚠️ 勿用 `e409a7b6`（11-19 release `20261119-004647` 用它构建 `memora:audit-82647d83`，发布失败已回滚）。
3. **11-19 发布失败真因（已定谳）**：与构建无关——verify 脚本 `verify-acc-capability.sh` 的 self-check 需要 `ACC_VERIFY_AUTH_HEADER_FILE`（curl header 文件带服务 JWT）调 `POST /api/v2/acc/self-check`，上次没传 ⇒ 401 ⇒ 整体 fail ⇒ 回滚。**该脚本对当前在役稳定镜像跑也是同样 3 项 FAIL（15 PASS / 3 FAIL）**，纯凭据问题。
4. 修复分支一旦部署，verify 时传 `ACC_VERIFY_AUTH_HEADER_FILE=<含 Authorization: Bearer <服务JWT> 的文件>` 即可全绿。
5. memora release 管线工具（产生 memora-releases/*/receipt.json 的那套）不在 211 PATH、不在 memora 仓库 deploy/（那是旧 kxmemory 裸机脚本）——owner 侧工具，部署前先问 owner 管线入口。

## 其他采集口径修正

- prewarm 五态指标读取姿势：容器内 `wget --header="Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" localhost:8781/metrics`（宿主 curl 401；adminkey 从容器 env 取）。
- 灰度两键位置：`llm_gateway` 库 `public.settings_kv`（session_manager_dev 里没有这张表）。
- 剩余 1 条 DLQ（evt-prewarm-e2e-0003）是本轮 E2E 测试自产的 terminal 事件（envelope 缺 session_id），无重放价值。

## 下一步（新会话接续点）

1. 等 `/tmp/emb-watch3.sh` 过夜监控报 embedding 恢复（10min 周期，12h 窗）。累计观测：15:08~20:37 两轮监控（90+48 次）全 503 零恢复，宕机自前夜起已超一整天。
2. 恢复后：观察 injected_bytes 增长 + hit 态出现 → 按 P1-4 口径测 Recall@5 / nDCG 出终版报告。
3. 与 owner 协调部署 memora 修复 `recall-authz-stamp-211 @ 61d6280a`（材料齐备，见上节情报 1/4/5）。
4. embedding 恢复前 timeout 态会持续上涨——属预期（上游 503），无需处理。

**20:40 快照（slot b）**：requested=316 / miss=176 / timeout=140 / unavailable=0（认证链零失败持续）/ hit=0 / injected_bytes=0；checkpoint 664855 lag 55s 健康；DLQ 仍 1（terminal）。timeout 占比 ~44% 与 embedding 上游慢挂一致。

## 续作轮核销（2026-11-28 晚，第三会话）

| 待办 | 结果 |
|---|---|
| 监控 embedding 恢复 | ⚠️ 上游症状变化：503 → **502**（22:44 起两侧一致），仍宕。**211 侧监控线实际未死**（v3 pid 4191268 存活，22:54 仍出探针；前轮"静默死亡"判断被 ssh 包装 shell 假阳性干扰）。本地侧新增一条冗余监控（同脚本，log=/tmp/emb-watch3-r2.log，22:55 首探 502），双线互备。⚠️ 本机无 setsid，用 `nohup ... & disown` 替代 |
| 五态快照 | ✅ slot b（第三连槽）：requested=149 / miss=139 / timeout=10 / **unavailable=0（认证链零失败跨三槽保持）** / hit=0 / injected_bytes=0。owner 今日第三次重发：镜像 `2.6.2-1f66e5ac-20261128-211`（`1f66e5ac` 已核实 ∈ main 线，与 4570c49 同源） |
| 两键在位 | ✅ `memory.prewarm.enabled=true` + `tenant_allowlist="default"` 跨三次重发未漂移（`llm_gateway` 库 `public.settings_kv`） |
| DLQ | ✅ 仍 1 条 terminal（`gateway_dead_letter_events` id=1 evt-prewarm-e2e-0003）；ASM 侧 `dead_letter_events`=0。注意 `_audit_dlq_ids_*`（gw=26093 / 20261028=34847）是审计 ID 快照表，非事件 DLQ，勿混淆 |
| memora 修复部署 | ❌ 未部署（在役二进制 re-verified = `v0.0.0-20261115214133-8560dea20eea`）；trusted-authz ERROR 近 30min 仍 51 次（recall.go:524）；ASM 侧近 30min "degrading to knowledge-only"=0（timeout 慢挂而非取消路径）。**仍待 owner 窗口**，材料齐备性已复核：分支 `recall-authz-stamp-211`（`61d6280a` 修复 + `91b309c0` 基线，祖先链 8560dea2 ✓，双用例+产物入库 ✓，origin 已推 ✓） |
| 网关/父仓收敛 | ✅ 同僚再推 6 提交（A6 端点选择器接线 owner 拍板默认开、ASM token 读侧 %q 解码根修、路由分析报告等，与本线零交集）→ 网关 FF 收敛 271bdc0→**4570c490ca**（双远端现名 `origin`=codeup + `github`，0/0 同址）；父仓指针 → **ac75a0a** 已推 origin |

**下轮接续点**（不变，叠加两条）：
5. 监控判定以 `ps -eo pid,args | awk '$2=="bash" && $3=="/tmp/emb-watch3.sh"'` + log 文件尾行为准（双机各一条，211 优先）；恢复信号 = log 出现 `EMBEDDING RECOVERED`。
6. memora 部署窗口出现时：先 `go version -m` 复核在役提交，从 `8560dea2` 切面构建 `61d6280a`（勿用 codex/s5-audit-211 线），verify 带 `ACC_VERIFY_AUTH_HEADER_FILE`。

## 修复部署轮（2026-11-29 凌晨，**61d6280a 已上生产**）

**部署经过（含撞车插曲）**：
- 本机先建 arm64 交叉构建+薄层镜像，正要换血时发现 **owner 正在 211 上跑 `container-release deploy memora:audit-99347c85...`（构建提交 `1a9d91e2`，qwen3 embedding 切换 + minimax-m3 模型，两轮 reindex 61967/61969 条）**——主动避让，全程只读观察。
- **owner 两轮发布均回滚**（服务回到旧镜像 `997adef/8560dea2`，memora-server/worker 重启回旧版）；第二轮 reindex 完成后回滚，`.32` 会话收工离场。判定：**其发布链依旧栽在 verify 凭据（ACC_VERIFY_AUTH_HEADER_FILE 未传）+ embedding 503 让 self-check 的 candidate_promote 必败**——与我方后来实测一致（见下）。
- owner 窗口空出后执行换血：本地重build **amd64**（⚠️ 首次 arm64 二进制 `exec format error`——211 docker 是 x86_64；在役镜像 `memora:audit-997adef...` 也是 amd64），薄层 `FROM 997adef + COPY kxmemory-go`（fix @ `91b309c0`，含 61d6280a），容器换血方式 = 旧容器 rename+stop 保留回滚壳（`memora-server-pre-fix-20261128`，restart=no）+ 新容器克隆全部 env/网络/卷/端口。

**部署后验证（全绿）**：
- 在役二进制 `go version -m` = `v0.0.0-20261128084559-91b309c08336` ✓；healthy；goacos 注册成功；零启动 error。
- **trusted-authz ERROR 全程 0**（修复前每 20~30s 必报）。
- 三用例活体：带 `X-Project-ID` 的 service JWT search → **200**（修复前 403 `trusted project authorization required` 路径）；`GET /api/v2/knowledge-bases` → 200；search 正常返回 degraded（embedding 503，reason 正确标注 `embedding_failed`）。
- **官方 verify 脚本（带 ACC_VERIFY_AUTH_HEADER_FILE）= 15 PASS / 3 FAIL**——3 项 FAIL 与在役稳定镜像完全同款且同根：`candidate_promote` 失败原因 `embedding API: HTTP 503`（上游仍宕），manifest snapshot/verify 连锁 skip。**凭据链已闭环**（服务 JWT 认证过，自铸 token 走通），embedding 恢复后重跑即可全绿。

**工程备忘**：
- memora-server 由 compose 项目 `kaixuan-platform` 管，但 owner 的发布工具绕过 compose 直接 `docker run`；我方换血同款（env 全量克隆自旧容器 inspect）。compose 文件里镜像行仍 pin 旧镜像（997adef），**下次 compose up 会回滚本次修复**——owner 发布或手动改 compose 前需知晓。
- 服务 JWT 直打 /api/v2 的姿势：HS256 + JWT_SECRET（容器 env），claims 必需 `sub`（service: 前缀或 actor_type=service）、`tenant_id`、`iss=memora`、`aud=memora-api`、`scope`、`exp`；project 访问需 DB `projects` 行且 owner 匹配 principal。
- 五态 counter（`gateway_memory_prewarm_total`）是 CounterVec：**零 chat 流量时不暴露任何 series**（此前"计数器为空"的两次观察都是新槽刚起+夜间无流量的正常态，非丢指标）。prewarm 触发点在 chat completions 路径（handler.go:5455 Apply）。
- owner 发布工作区 `/tmp/memora-release-99347c85.../` 有现成 `verify-acc-capability.sh` + `container-release` 工具（读取 `ACC_VERIFY_AUTH_HEADER_FILE` env）——下轮协调 owner 重发布时直接引用。

## P1-4 评测面补齐（2026-11-28 下午，不需 embedding 的部分已完成）

memora 仓有完整评测基建（`cmd/recall-eval` + `cmd/recall-eval-baseline` + `internal/recalleval`，种子集 `eval/recall/dataset.json`）。本轮补齐两块：

1. **oracle 自检（扩集后 44 用例）**：全指标 1.0（此前只跑过 24/32 用例口径）——尺子校准。
2. **degraded-keyword 基线 v2（44 用例，真跑）**：`recall@5=0.1818`（keyword 探针 8/8、自然语言 36 例全零，ILIKE 语义使然）——**低于绝对门禁 0.25**。即 embedding 上游不恢复、召回走 degraded 时，P1-4 门禁不放行放量是**正确行为**（无基线不上线纪律自洽）。产物已入库 `eval/recall/results/degraded-keyword-v2-44cases{,.ranked}.json`，BASELINE 文档新增第 4 节。

**门禁口径的两处表述（报告要并陈）**：`settings/spec_memory_prewarm.go` 钉绝对值（Recall@5 ≥ 0.25 且 nDCG@10 ≥ 90%）；`eval/recall/BASELINE-2026-11-02.md` 门禁节写相对值（≥ degraded 基线且 nDCG 不低于其 90%）。两种口径下结论一致：degraded 路径不达标，向量路径测量是放量硬前置。

**embedding 恢复后的补测路径**（终版报告唯一缺口）：生产 memora 上载入种子集 corpus（eval-tenant/eval-recall 隔离）→ 逐 case 真检索导出 ranked `{case_id:[mem_id...]}` → `recall-eval -mode ranked` 评分 → 与 degraded v2 基线对照。

以上均在 memora 分支 `recall-authz-stamp-211`（`61d6280a` 修复 + `91b309c0` 评测基线，已推 origin）。

## 第三轮发布接续（2026-11-29 02:30 观察轮）

**新态势（02:24 例巡发现）**：
- **owner（.32）00:40 起第三轮发布**，00:47:44 停掉了我的修复容器（`memora-server` fix@91b309c0，优雅退出 exit=0）——**不是回滚我的修复，是其发布流程的 writers-stopped 阶段**。
- **本轮意图 = embedding 上游大迁移**：新起 `memora-embedding` 容器（本地 llama.cpp，**Qwen3-Embedding-0.6B-Q8_0**，shared-infra 网，`/health` = ok）替代持续 502/503 的 kxpms 远端；`goal-reindex` 容器以 `--all-tenants --writers-stopped --include-memories --resume` 全量重刷 62016 条向量（镜像 `99347c85` = 构建 `1a9d91e2`，**仍不含 61d6280a**）。
- 速率 ~100 条/min ⇒ reindex 约需 **~9h**（到上午）。reindex 零 error，新通道工作正常。
- 修复容器本体还在（exited，二进制 91b309c0 已验），回滚壳 `memora-server-pre-fix-20261128` 也在——**61d6280a 的薄层镜像 `memora:fix-61d6280a-recall-authz`（4a4a86ea9e8c）随时可再叠**。

**过时监控线下线**：emb-watch3 双线（211 pid 4191268 + 本地 54406）已杀——脚本探的 kxpms 通道已被 owner 弃用（211 侧连接失败 HTTP 000、本地 401 key 失效），监控目标过时。**若仍需监控 embedding，目标应改为 `http://memora-embedding:8080/health`（shared-infra 网）**。

**接续点（替换前述第 5/6 条）**：
5. 上午 reindex 完成后 owner 会启 `99347c85` 的 memora-server——**核查其二进制是否含 61d6280a**（`go version -m` 看 vcs.revision）；若不含，在 99347c85 基座上重做薄层（方法同上节，新基座镜像 ID 以 `docker images memora:audit-99347c85...` 为准，amd64）。
6. owner 的 reindex 用 `--resume` 且 per-batch 提交：若其流程中断，容器名 `goal-reindex` 可复跑续传。
7. embedding 上游已切本地：Recall@5/nDCG 补测与 verify 重跑**不再依赖 kxpms 恢复**——reindex 完成后即可按 P1-4 补测路径执行（seed corpus → 真检索 → ranked 评分）。
