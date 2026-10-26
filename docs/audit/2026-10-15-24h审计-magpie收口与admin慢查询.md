# 2026-10-15 24h 审计轮 —— magpie P0 收口合入 + admin 慢查询根修

> checkout：`llm-gateway-go-3`。基线 `origin/main` 于开跑时为 `149d6fc91`。

## 一、分支面

| 窗口 | 结果 |
|---|---|
| 24h 内、≥1h 未活动、未合并远程子分支 | **0** |
| 本地已合并陈旧分支（ahead=0） | 10 条，本轮删除 |
| `cursor/magpie-p0-closeout` | 活跃（<1h），含 P0-B/C/D 收口 + 判据；已 `--no-ff` 合入 main |
| `r43-fresh-install-prereqs-and-startup-gates` | 窗口外（9-30）、ahead=1；含删 704/705 embed 的危险 diff，**留置不合并** |

## 二、24h main 提交批判摘要（按主题，非逐 SHA）

1. **magpie P0 / streaming**（`ed620001e`→`9ea45f7f8`+判据）：CoolUntil 单飞、quotaHeld、breaker×PenaltyOf、Anthropic 剥参契约——方向正确；本轮抓到进程级 `defaultMemoryProbeOneFlight` 跨测试污染（见 §三 D1）。
2. **S4 / compression r1031**（`9a66472f1`/`6db2d18ed`）：L2 复水 hash 与审计门——与本轮日志无新冲突。
3. **迁移 863/864**：handoff 形态自适应 + `model_aliases` 部分索引——生产台账已有 860–864。
4. **URSM persist 留存**（`24dd83c14` 等）：DROP 互斥与 30→7d——文档续写正确；`REFRESH routing_analytics_7d` 仍是 pg_stat_statements 总耗时冠军（既有登记）。
5. **mobile 权限档连批**：UI 接入为主，服务端角色下限债登记在 commit message——本轮未展开。
6. **标题/$2 + liveness retired**（`f1d31b580`）：生产未再扫到 `unused argument`；liveness 噪声需部署后确认。

## 三、本轮真缺陷与修复

### D1 — probeOne 进程级单飞污染包测（P0 收口回归）

- **现象**：`TestExecutor_NoCandidateProbe_HonorsClientCancel` 单独绿、与同包 NoCandidate/ProbeOne 连跑红（ProbeSync never called）。
- **根因**：`defaultMemoryProbeOneFlight` 进程单例 + `TryAcquire(..., holdTimeout)` 无 Release → 同 model 锁跨测试残留。
- **修**：per-Executor 惰性 `MemoryProbeOneFlight`；删进程单例；钉子 `TestDefaultProbeOneFlightIsPerExecutor`。

### D2 — `/api/admin/sessions/online` 5s 超时 500（生产）

- **现象**：154 journal `session_online.go:133` `context deadline exceeded`，dashboard 连带 500。
- **根因**：`session_last_requests` ⋈ `request_logs_with_current_month`；停写后视图臂 `id` 对 session 行为 `NULL::bigint`，slr 表 **0 行**，EXPLAIN ANALYZE **52.8s / 0 行**。
- **对照**：`session_summaries` + `idx_session_summaries_tenant_time` 近 1h 窗 **0.76ms / 180 行**。
- **修**：online 列表改读 `session_summaries`（1h 窗 + tenant + cursor）；inventory/cutover 登记出列；源码钉子。

### D3 — `/api/logs/top-models` 10s 超时 500（生产）

- **现象**：dashboard 3d 窗 `duration_ms≈10014` → 500。
- **背景**：`REFRESH MATERIALIZED VIEW CONCURRENTLY routing_analytics_7d` mean≈26s，与短查询争用。
- **修**：≤8d 窗改 `SessionFamilyTurnsHotSourceSQL()`（仅 hot 腿）；长窗仍全族。钉子更新。

## 四、系统/DB 日志其它观察（登记不修或非本轮）

| 信号 | 判定 |
|---|---|
| `session_cache: db load error` / `no rows` | 冷会话 miss，WARN 噪声；非故障 |
| `fp_slot_saturation_critical` claude-opus | 既有指纹槽；owner |
| `model alternatives lookup` timeout | 与视图/争用同族；已有会话族迁移测试，留观 |
| `REFRESH routing_analytics_7d` 总耗时第一 | 既有 URSM/MV 债；非请求热路径本次未动调度 |
| `analyze_llm_gateway_table_stats` mean≈140s | 产品作业 |
| 154=`2637-6db2d18e` / 245=`2634-f280c9dc` | **本轮代码未部署**；上线后 online/top-models 需复验 |

## 五、验证

```text
CGO_ENABLED=0 go build ./...                         # RC=0
CGO_ENABLED=0 go vet ./...                           # RC=0
CGO_ENABLED=0 go test ./domains/streaming/executors/ # RC=0（含污染复现组）
CGO_ENABLED=0 go test ./admin/ ./db/ -run 'TestSessionsOnlineSQL|TestListTopModelsSQL|TestRequestLogsReadInventory|TestViewArmCutover'
bash tests/observe_magpie_p0_contract_test.sh        # ALL PASS
```

生产 SQL 对照（252 `pg-252-pg17` / `llm_gateway`）：见上文 EXPLAIN 数字。

## 六、下一轮提示词（部署前口径；已被 §七 收口）

1. ★ **部署本轮**（154/245/211）：magpie P0-B/C/D + D1/D2/D3；复验 `/api/admin/sessions/online` 与 `/api/logs/top-models` 亚秒、dashboard 无 500。
2. **MV REFRESH 争用**：`routing_analytics_7d` 降频或 CONCURRENTLY 窗口错峰（URSM §10.110 续）。
3. **`r43-fresh-install`** 门/脚本是否拣有用文件合入（禁止带删 704/705）。
4. **model alternatives** 超时与 nvidia probe 502：owner。
5. 窗口外九月远端三分支留置。

## 七、部署执行收口（2026-10-15 本 checkout `llm-gateway-go-3`）

> 构建锚点：`6dc0ad45`（含 peer 已上线的 D1/D2/D3；本会话叠加 MV 降频 + cursor HMAC 回落 + 211 脚本硬化）。
> **未授权 commit**——工作区改动仍脏，待 owner 一句话。

### 7.1 本会话代码增量（未提交）

| 项 | 变更 |
|---|---|
| MV 降频 | `RefreshInterval` 10m→**30m**；`mvFreshnessBudget` 15m→**45m**；算术钉子两侧夹住 |
| cursor HMAC | `CURSOR_HMAC_SECRET` 缺/短时回落 `LLM_GATEWAY_SECRET_KEY`（≥32）；`TestCursorFallsBackToGatewaySecretKey` |
| r43 门拣选 | 只合三门测试 + `scripts/audit/{map_creators,scan_startup_chain}.py`；718/719 **不注销**，进 concurrency 例外表 |
| deploy-211 | `D211_REPO_ROOT`；禁裸 `--exclude=build`；新鲜度排除 `*.test/spec`；`COPYFILE_DISABLE` + `--no-xattrs`；半包后脚本存在门；契约 **46/46** |

### 7.2 部署版本与端口

| 目标 | version | active_port | 备注 |
|---|---|---|---|
| **245** | `2.5.8-6dc0ad45-20261015-**2650**` | **8781** | `deploy-245.sh` |
| **154** | `2.5.8-6dc0ad45-20261015-**2651**` | **8782** | `deploy-154.sh`；等 peer 2642 锁后切流 |
| **211** | `2.5.8-6dc0ad45-20261015-**2645**` | 8781（容器） | `deploy-211-docker.sh --via`；seq 计数器独立 |

### 7.3 复验矩阵（实测）

| 端点 / 探针 | 245 | 154 | 211 |
|---|---|---|---|
| `/api/admin/sessions/online` | 200 **102ms**（首轮冷 423ms） | 200 **114ms** | 200 **6.6ms** |
| `/api/logs/top-models?days=3` | 200 **332ms**（首轮冷 1156ms 一次） | 200 **337ms** | 200 **11.1ms** |
| magpie 四指纹 | OK（`TARGET=245 JOURNAL=1`） | OK（`TARGET=154 JOURNAL=1`） | OK（容器 `llm-gateway` 字节） |
| readyz | ready :8781 | ready :8782 | ready :8781 |

magpie 30m 日志计数本窗全 0（无风暴信号；脚本本身不宣称「全绿」）。

### 7.4 运维副作用

1. **online 首轮 503 `pagination cursor unavailable`**：生产缺 `CURSOR_HMAC_SECRET`。处置=代码回落 SECRET_KEY + 245/154 `/etc` 注入 64 字节；154 `/opt/.env` 曾留下 **空值 CURSOR 行**（len=0），已从 `/etc` 回填并备份 `.env.bak-cursor-*`。
2. **211 admin**：`gateway.env` **无** `LLM_GATEWAY_ADMIN_*`（设计内——users 表继承）；用本机 `.env.local` 同账号登录成功。
3. **deploy 产物脏树**：`VERSION` / `version.json` / `web/public/*` / `docs/db-changelog.md` 已 `git checkout --` 还原，勿混进提交。

### 7.5 下一轮提示词（部署后）

1. **owner 授权后 commit + push** 本会话脏集（MV/cursor/r43 门/211 脚本）；勿带部署产物。
2. **deploy-211 续优化**（已硬化 xattr/半包/REPO_ROOT；仍欠）：把 `LLM_GATEWAY_ADMIN_*` 可选写入 `gateway.env`（或文档钉「用 .env.local 同库账号」）；observe 脚本补 `TARGET=211` 容器路径；claim/URSM 已有自占复用，冷启动后 emergency-repair 是否仍需人工留观。
3. **MV 争用**：30m 降频已上线——用 pg_stat_statements 对比 `routing_analytics_7d` 总耗时是否下降；不足再错峰。
4. model alternatives 超时、fp_slot、nvidia probe：owner。
5. 窗口外 `r43-fresh-install*` / 九月远端三分支：留置。

## 八、复审 + 三脚本重部署（2026-10-16 02:2x–02:5x）

### 8.1 部署前复审（§七 之后生产已被并行会话改动）

| 目标 | 复审时实况 | 判定 |
|---|---|---|
| 245 | `2664-9a649cbe` :8782，指纹全在 | 正常 |
| 154 | `2655-d0d3d1f9` :8781，**缺 cursor 回落**与 MV 30m（env 有 CURSOR_HMAC 兜着，online 200） | 落后 |
| 211 | 10-15 14:57 被重建为 **`2636-f280c9dc`**（早于 §七 的 2645），缺 cursor 回落，`gateway.env` 无 `CURSOR_HMAC_SECRET` ⇒ **`/api/admin/sessions/online` 503 `pagination cursor unavailable`** | **线上故障** |

§七 的 MV/cursor/r43/211 改动已由他人随 `c708260d0` 一带提交入 main，工作区干净。

### 8.2 部署结果（HEAD 拉到 `5b3a98da6`，部署中并行会话推进到 `75bf327aa`）

| 目标 | 脚本 | version | 端口 | 备注 |
|---|---|---|---|---|
| 245 | `deploy-245.sh` | `2662-5b3a98da` | 8781 | 本机 go 1.27.1 < go.mod 1.27.2，自动回退容器 CGO 构建；handoff 35s |
| 154 | `deploy-154.sh` | `2663-75bf327a` | 8782 | handoff 46s；比 245 多一个只加启动失败日志的提交 |
| 211 | `deploy-211.sh` | `75bf327a-dirty-…-211` | 8781 | 首跑 FATAL（见 8.3），修脚本后连跑两次均成功 |

### 8.3 deploy-211 两处缺陷（已修，未提交）

1. **URSM 库号逐次下移直至 die**：会话库 claim 已有「自占复用」，URSM 库没有——上次分配的库里有 `ursm:*` 键即判占用，每次部署下移一号（实测 db0～db4 都是 `ursm:` 残留，0～15 全有键）⇒ `无法为 URSM 分配独立 Redis db`。修：先读回 `gateway.env` 里的 `URSM_V2_REDIS_DB`，与会话库不撞号且库空或只含网关键即复用。连续两次部署均复用 db0（不漂移实证）。
2. **版本戳冒用**：引擎原样种子仓库 `version.json`（他人部署留下的戳或提交时旧值），`/version` 显示的不是 211 实际构建。修：`--via` 转发 `git rev-parse --short=8 HEAD`（脏树加 `-dirty`），远端改写为 `<tag>-<sha>-<date>-211`。

契约 `tests/deploy_211_contract_test.sh` **63/63**；新增 URSM 复用 4 条（行为驱动，截取脚本原段）+ 版本戳 2 条。变异：去掉复用 ⇒ 「全库被占」那条红成 `DIED`（与生产同形）；改写不改 sha ⇒ 版本戳那条红。

### 8.4 复验

| 项 | 245 | 154 | 211 |
|---|---|---|---|
| 三处对齐 + readyz | ✓ | ✓ | ✓（容器 healthy，mode=full） |
| 指纹（cursor 回落/probeOne/retry_after） | ✓ | ✓（另含 `accepted configurations`） | ✓（同 154） |
| online / top-models 热态 | 80 / 135ms | 102 / 112ms | 1.6 / 18ms |
| 伪造 sk-* `/v1/models` | 401 | 401 | 401（无 key 也 401） |
| URSM 调和器 | — | — | **`reconciler started` 首次在 211 出现** |
| 公网 | `llm.kxpms.cn/readyz` 200 | 同 | `llm.itestu.cn/readyz` 200；root SPA 200、`/m/` 200 |

### 8.5 新发现（登记，未修）

1. **online 超管路径冷查询可超 5s（245 两次实测 500）**：超管不带 `tenant_id`，PG 只能借 `idx_session_summaries_tenant_time` 跳过前导列整扫（72.7 万行表取 172 行碰 3254 页，热态 96ms）；252 load 15–22 时冷缓存首查越过 5s 预算。根修需 `session_summaries(last_request_at DESC, session_key DESC)` 新索引（新迁移 + 五点同步 + 通道腿），待 owner。同时段 `pending_sweeper`/`writeHealth`/`model alternatives` 也在超时，252 负载是共因。
2. **211 启动 WARN `auth: INSECURE fail-open mode`**（未设 `LLM_GATEWAY_ENV=production`）：实测数据面伪造/无 key 均 401，未见放行；是否给 211 注入 `LLM_GATEWAY_ENV=production` 待 owner。
3. **本机 Go 1.27.1 落后 go.mod 的 1.27.2**：deploy-seamless 自动回退 amd64 容器 CGO 构建（245 一次约 8 分钟）；升级本机 Go 可省时间。
4. Redis（nbjl-redis）db0～db4、db6～db15 有网关历史漂移残留键；不影响运行，清理属删数据，未动。

## 九、优化轮（2026-10-16 03:2x–03:4x）—— §8.5 四项处置

### 9.1 online 超管冷查询 → 迁移 867

- 索引：`session_summaries (last_request_at DESC, session_key DESC) WHERE last_request_at IS NOT NULL`，键序与 `admin/session_online.go` 的 ORDER BY 逐列同向。
- **大表上线方式**：252 表 73.6 万行 / 760MB、持续写；启动链 `psql --single-transaction` 不能 CONCURRENTLY。因此生产由运维先 `CREATE INDEX CONCURRENTLY` 同名预建（32s，valid，49MB）；迁移体把 CREATE 包进 `to_regclass(...) IS NULL` 判定块。
- **判定块的必要性已实测**（一次性 PG17，30 万行）：写事务持锁时，索引已存在的环境裸跑 `CREATE INDEX IF NOT EXISTS` 仍 2s 锁超时（PG 在发现同名索引前已拿 ShareLock）；867 本体 0s 完成。首跑 / 重放 / down→up 往返全过，规划器走 Index Only Scan。
- 效果：252 EXPLAIN 102ms / 3,974 页 → **0.22ms / 19 页**（无排序节点）；245、154 端点首调 4–21ms，此前冷调用 >5s 并 500。
- 同步：canonical + embed + main.go + runner.go + 通道腿 + TSV(262) + db-changelog；判据 `migration_867_test.go` 8 钉。变异：去判定块 / 加回租户前导 / embed 漂移三方向均红在对应断言。门：installer 全绿、`apply-db-revision-sequence_test` rc=0、`internal/sqlguard` ok。

### 9.2 211 `LLM_GATEWAY_ENV=production`

- 生产模式启动门 `config.ValidateAuthSecrets` 要求 API_KEY / ADMIN_API_KEY / JWT（可回落 SECRET_KEY）/ ≥32 字节 CURSOR_HMAC_SECRET；211 只有 SECRET_KEY，直接加 production 会 panic。
- 154/245 **也未设** `LLM_GATEWAY_ENV`，无告警只因四件齐全。
- `deploy-211-docker.sh`：`d211_auth_secret` 按「环境变量 > env/gateway.{apikey,adminkey,cursorhmac} > 首次生成」取值，重跑不轮换；静态 key 前缀 `gw211_`（sk-* 会被委托库校验）、admin `ops_`；容器 -e 与 gateway.env 同步注入，加 `LLM_GATEWAY_ENV=production`。
- 影响面：`/healthz/full`、`/metrics`、`/metrics/storage`、`/admin/config/reload`、`/internal/*` 改为需 ops token（此前 211 上不鉴权）；部署就绪探测用的 `/readyz`、`/healthz` 不受影响；211 无抓取方（日志里 `/metrics` 只有启动告警）。
- 契约 63 → **76/76**（bash 5 与 3.2 均绿）；变异：不读持久值 / sk- 前缀 / 去 production 注入三方向均红。**未部署**，见 9.4。

### 9.3 本机 Go

`brew upgrade go` 1.27.1 → **1.27.2**（官方最新稳定，=go.mod）；`GOTOOLCHAIN=local CGO_ENABLED=0 go build ./...` rc=0。deploy-seamless 不再回退容器构建，无需改脚本。

### 9.4 未做 / 待 owner

1. **部署**：并行会话 02:3x 合入「版本化脱敏规则管理」（61 checkpoint + 迁移 866），并已部署 245=`2664-dd0d8248`(:8782)，866 的表已在共享库。154 仍 `2663-75bf327a`、211 仍旧版；此时部署任一台都会连带该功能，故本轮只提交推送、不部署。867 对 154/245 的收益已经通过预建生效；211 的 production 改动与 867 普通构建需下次部署。
2. **Redis 漂移残留键**（§8.5-4）：属删数据，须影响分析 + 二次审计 + 人工确认，本轮未动。
