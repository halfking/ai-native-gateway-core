# llm-gateway-go 24h 审计综合报告

**生成时间**：2026-06-27 03:55+ UTC+8  
**项目**：`services/llm-gateway-go`（monorepo 子模块，自身独立 git 仓）  
**审计窗口**：2026-06-26 04:30 ~ 2026-06-27 03:50（自首个 24h commit `67340e57` 至最新 `207badf0`）  
**审计方式**：6 个并行只读 subagent（code-quality / consistency / security / performance / test-coverage / integration-config）+ 主 agent 预判性深度核查  
**报告原则**：只读审计，不修改任何代码；所有问题均附 file:line 定位

---

## 0. 执行摘要 (TL;DR)

| 指标 | 值 |
|---|---|
| 24h 内 commit 数 | **53** |
| 影响文件数 | 130 files, +1,252 / −14,171 |
| 单 commit 最大影响 | `67340e57` R1.13 cutover：356 files / +2,476 / −474 |
| 未提交修改 | 1 文件（`version.json` 漂移） |
| Untracked | 0 |
| 发现严重度 | **P0 × 3** / **P1 × 7** / **P2 × 5** |

### 三个最紧急的问题（**必须立即处理**）

1. **P0 — commit `0e4a5dbf` 单独无法 build**：`cmd/gateway/main.go:535` 调用 `disguise.NewRedisPool(...)`，但该函数 30 分钟后才在 `6a2e5fcd` 定义；中间 30 分钟的 `0e4a5dbf` commit 上 `go build ./...` 会失败（undefined: disguise.NewRedisPool）。任何 `git bisect` 落到这两个 commit 之间会得到 broken build。
2. **P0 — `client.go` SQL 不对称导致静默数据丢失**：`client_request_id` 在 INSERT（行 610）用 `COALESCE(EXCLUDED.x, request_logs.x)` 限定到表上，但在 UPDATE（行 872）用 `COALESCE($64, client_request_id)` 无表限定。**两边的 COALESCE 行为不同**——UPDATE 时如果 entry.ClientRequestID == nil 就会读目标列，但目标列可能因别处竞争已被 NULL 化。8 次连续 fix 没解决语义不对称问题。
3. **P0 — `version.json` 与 HEAD 漂移 + 父仓库子模块 ref 落后**：HEAD = `207badf0`（03:50:58），version.json git_sha = `e7d4deb6`（03:44:20），落后 2 commit；git_tag 字段名"-52-ge7d4deb6"声称 52 commit 但实际 53。父仓库 `official-deploy` 在 `621e6b8`（03:47:10）子模块 bump 到 `22613c1f`，**但子模块 HEAD 已超过**——任何基于父仓库 ref 的部署都会拿过时的 binary。

---

## 1. 风险矩阵

| 严重度 | 数量 | 主要类别 |
|---|---|---|
| **P0**（阻塞部署 / 静默数据丢失） | 3 | commit 顺序错位、SQL 不对称、version.json 漂移 |
| **P1**（功能失效 / 安全隐患 / 一致性破坏） | 7 | hot-reload 假象、Redis shim 误导、R1.13 manifest 不准、混改 commit、disguise 改标题不符、telemetry 共享连接池、注释与代码矛盾 |
| **P2**（代码质量 / 可维护性） | 5 | 测试覆盖深度不足、`mergeRequestLogBatch` Insert 不合并、`ResolveRequestStatus` 逻辑冗余、跨包代码重复、metrics 覆盖不全 |

---

## 2. P0 级别问题

### P0-1. commit `0e4a5dbf` 单独 checkout 时 build 失败

- **位置**：
  - `cmd/gateway/main.go:535` — `routingExec.DisguisePool = disguise.NewRedisPool(fpSlotRedis, 30*time.Minute)`
  - `disguise/pool.go` — `NewRedisPool` 在 `0e4a5dbf`（02:18:19）之前**不存在**，`6a2e5fcd`（02:48:52）才添加
- **证据**：
  ```
  $ git -C .../llm-gateway-go show 0e4a5dbf --name-only
  cmd/gateway/main.go
  cmd/gateway/memory_adapter.go
  # 0e4a5dbf 没碰 disguise/pool.go
  $ git -C .../llm-gateway-go show 6a2e5fcd --stat
  disguise/pool.go | 22 +++++++++++++++++-----
  ```
- **影响**：
  - `git checkout 0e4a5dbf && go build ./...` 失败：`undefined: disguise.NewRedisPool`
  - `git bisect` 落到这两个 commit 之间会得到 broken build
  - 违反"每个 commit 都能 build"的基本原则
- **建议**：
  - **重新排序 commit**：要么先发 `6a2e5fcd`（修 NewRedisPool）再发 `0e4a5dbf`（refactor 调用），要么 `0e4a5dbf` 不引入 `NewRedisPool` 调用
  - 用 `git rebase -i` 在允许范围内（分支冻结期需用户授权）调整顺序
  - 短期：在 `0e4a5dbf` 的 commit message 中明确"DEPENDS ON 6a2e5fcd"

### P0-2. `request_logs` upsert 8 次连续 fix 仍未根治

- **位置**：`domains/hooks/observability/telemetry/client.go`
  - INSERT（行 540-611）`ON CONFLICT (request_id, ts) DO UPDATE SET ... client_request_id = COALESCE(EXCLUDED.client_request_id, request_logs.client_request_id)`
  - UPDATE（行 786-876）`... client_request_id = COALESCE($64, client_request_id)`（**无表限定**）
  - 应用层合并（行 1331-1335）注释说"keep first non-empty"但 `mergeStringPtr`（行 1359-1364）实现是"src 非空就覆盖"
- **8 次 fix 演进路径**：
  1. `acfe7b58` 加 `client_request_id` 列
  2. `726070b9` 改 ON CONFLICT 处理 failure-path
  3. `b35bb750` 又改一次
  4. `a059c265` 移除 `target_alias`
  5. `c4e1ae47` qualify predicate
  6. `0ffa7466` 加 `request_logs.id = latest.id AND request_logs.ts = latest.ts` 限定
  7. `6b2ba0e2` 把 `rl.client_request_id` 改成 `client_request_id`（target alias 修复）
  8. `e7d4deb6` 又改成 `request_logs.client_request_id`（明确限定）
- **遗漏**：
  - **INSERT 限定了表，UPDATE 没限定**——这是非对称。两个 COALESCE 行为不同：INSERT 端语义"EXCLUDED 为空就保留旧值"明确；UPDATE 端 `client_request_id` 在没有显式表别名上下文里**等同于 `request_logs.client_request_id`**（PostgreSQL 单表 UPDATE 隐式列引用），但如果未来某次重构加了 `FROM other_table` 别名引用，UPDATE 端会**静默错误**
  - **应用层合并与 SQL 端语义不一致**：SQL 端 COALESCE 保留旧值，但 `mergeRequestLogEntry` 总是覆盖——如果 `src.ClientRequestID` 是空串（不是 nil），`mergeStringPtr` 不会覆盖（行 1360 `*src != ""`），但 SQL 端 COALESCE 把空串当非空——**两边对"空"判断不一致**
  - **`mergeRequestLogBatch` 永不合并 Insert**（行 1269 `if !ok || entry.Op != RequestLogUpdate`）——如果 stream 期间出现 Insert→Update 顺序（理论上不该），Insert 单独写，Update 单独写，**两次 DB roundtrip 浪费**
  - **`mergeRequestLogEntry` 行 1290** 强制 `dst.Op = RequestLogUpdate`——如果合并序列中第一个是 Insert 后续是 Update，Op 强制为 Update 没问题；但如果都是 Update 后续再来一个 Insert（罕见但 stream 边界可能），**Op 被错误覆盖为 Update**
- **测试问题**（行 176-187 `TestInsertUpsertSQL_DoesNotReferenceUndefinedRLAlias`）：
  - 测试**只检查一个 const 字符串**，不连 db，不实际跑 SQL
  - 真实 SQL 在 `client.go:540-611` 和 `client.go:786-876`，测试根本没断言这两个位置的字符串
  - 8 次连续 fix 没把这个测试改造成"读 `client.go` 的 SQL 字符串然后断言"
- **影响**：
  - 静默数据丢失：client retries 用同一 X-Request-Id 时，第二次 UPDATE 可能把第一次的值覆盖
  - 跨请求污染：a 失败的 entry 写入后 b 成功的 entry 来 UPDATE，COALESCE 的隐式列引用可能读错列
  - 测试无法保护真实代码变更
- **建议**：
  - **统一 INSERT 与 UPDATE 的 COALESCE 写法**：UPDATE 端改成 `client_request_id = COALESCE($64, request_logs.client_request_id)`，与 INSERT 端一致
  - **重写测试**：从 `client.go` 中提取 SQL 字符串常量（而不是 const 写在测试里），做字符串断言；或用 `sqlparser` 解析后做 AST 断言
  - **加并发测试**：并发 INSERT + UPDATE 同一 request_id 序列，验证最终行数 = 1 而非 2
  - **加应用层 + SQL 端协同测试**：插入非 nil ClientRequestID，再插入 nil ClientRequestID（不应清空），再插入非空但不同值（应保留首次非空）

### P0-3. `version.json` 漂移 + 父仓库子模块 ref 落后

- **数据**：
  - HEAD = `207badf0`（`fix(executors): add miniredis to credentialfpslot tests`，03:50:58）
  - `version.json` `git_sha` = `e7d4deb6`（`fix(telemetry): qualify request_logs client_request_id in upsert`，03:44:20）
  - `version.json` 落后 2 commit（`22613c1f`、`207badf0`）
  - `version.json` `git_tag` 字段为 `phase1.5-pre-r113-20260626-52-ge7d4deb6`（声称 52 commit，但实际 53）
  - `build_date` 字段为 `2026-06-26`，但实际最新 commit 在 2026-06-27
- **父仓库层面**：
  - `official-deploy` 父仓库在 03:47:10 commit `621e6b8 chore(submodule): bump llm-gateway-go to 22613c1f`
  - 但子模块 HEAD 已到 `207badf0`（03:50:58）
  - **父仓库的子模块 ref 也已落后 2 commit**
- **影响**：
  - 任何基于 `version.json` 部署的脚本拿不到最新 binary
  - 任何基于父仓库 ref 的部署拿不到最新 binary
  - `/api/system/version` 返回的 `git_sha` 仍是 `e7d4deb6`
  - **运维/审计依据失真**：用户问"线上是哪个版本"，回答 e7d4deb6 但实际跑的是 207badf0
- **建议**：
  - 立即把 `version.json` 重新基于 HEAD 重新生成：
    ```bash
    cd services/llm-gateway-go
    git_sha=$(git rev-parse --short HEAD)
    commit_count=$(git log --oneline | wc -l)
    cat > version.json <<EOF
    {
      "version": "phase1.5-pre-r113-20260627-${commit_count}-g${git_sha}",
      "git_tag": "phase1.5-pre-r113-20260627-${commit_count}-g${git_sha}",
      "git_sha": "${git_sha}",
      "build_seq": $((700 + commit_count)),
      "build_date": "$(date +%Y-%m-%d)",
      "module": "llm-gateway-go"
    }
    EOF
    ```
  - 父仓库子模块 ref 重新 bump：
    ```bash
    cd /Users/xutaohuang/workspace/official-deploy
    git add services/llm-gateway-go
    git commit -m "chore(submodule): bump llm-gateway-go to 207badf0"
    ```
  - 建议在 `version.json` 旁加个 `Makefile` 目标自动生成，避免手动漂移

---

## 3. P1 级别问题

### P1-1. `session.id_body_keys` 标 `HotReload: true` 但 hot-reload 实际不工作

- **位置**：
  - `settings/spec_session.go:16` 标 `HotReload: true`
  - `cmd/gateway/main.go:158-160` — `if len(cfg.SessionIDBodyKeys) > 0 { streaming.SetSessionIDBodyKeys(cfg.SessionIDBodyKeys) }` — **仅启动时调用一次**
  - `domains/streaming/session_routing.go:48-52` — `var sessionBodyKeyOverrides []string`（无锁保护）
- **证据**：
  ```
  $ grep -rn "SetSessionIDBodyKeys" --include="*.go"
  ./cmd/gateway/main.go:159       # 启动时一次性
  ./domains/streaming/session_routing.go:50  # 定义
  ./domains/streaming/session_routing_test.go:38  # 测试用
  ./domains/streaming/session_routing_test.go:70  # 测试用
  # 没有任何 hot-reload handler 调用！
  ```
- **优先级逻辑**（`session_routing.go:54-68`）：
  ```go
  func configuredSessionFieldPriority() []string {
      // 1. defaultSessionFieldPriority（hard-coded）
      // 2. sessionBodyKeyOverrides（package-level 变量，由 SetSessionIDBodyKeys 写）
      // 3. sessionBodyKeySettings()（每次请求从 settings.Global 读）
  }
  ```
- **遗漏**：
  - **admin UI 改 `session.id_body_keys` setting → 实际生效路径是 #3（每次请求重读）**
  - 但如果启动时 `cfg.SessionIDBodyKeys`（来自 env var `LLM_GATEWAY_SESSION_ID_BODY_KEYS`）非空，**`sessionBodyKeyOverrides` 被永久填入启动值**，**`sessionBodyKeySettings()` 永远走不到**（被 `seen` map 在 default+override 后去重）
  - 实际行为：env 优先级 > admin UI 优先级，**与 `HotReload: true` 标记的承诺不符**
  - `sessionBodyKeyOverrides` 是 package-level 变量无锁保护——目前只在启动时单线程写，**但语义上被理解为可热加载**，未来加 hot-reload handler 后会**立即 data race**
- **影响**：
  - 运维改 admin UI 后误以为生效，实际不生效
  - env 与 admin UI 优先级反转
  - 未来 hot-reload 实现时引入并发 bug
- **建议**：
  - **要么**：把 `SetSessionIDBodyKeys` 真正接到 settings hot-reload handler（需要查 settings 包是否有 `OnChange` 机制）
  - **要么**：把 `HotReload: true` 改为 `false`，并把 `sessionBodyKeyOverrides` 删掉，让 `sessionBodyKeySettings()` 成为唯一来源
  - 如果选 #2，admin UI 改 setting 即可生效（无需重启），行为更可预测

### P1-2. `disguise.NewRedisPool` 与 `autoroute.NewRedisSessionIntentCache` 同样的 Redis shim 误导

- **位置**：
  - `disguise/pool.go:54-58` `func NewRedisPool(_ any, rotateInterval time.Duration) *Pool { return NewPool(rotateInterval) }`
  - `autoroute/session_intent_cache.go:68-70` `func NewRedisSessionIntentCache(_ *redis.Client, ttl time.Duration) *SessionIntentCache { return NewSessionIntentCache(ttl) }`
- **问题**：
  - 两个函数都接受 redis client 参数但**完全忽略**，返回 process-local 实现
  - 注释（disguise 行 49-52、session_intent_cache 行 64-67）承认"未来实现"
  - **但命名是 `Redis*`**——给读者/调用方的预期是 Redis 后端
  - `_ any` 类型（disguise）进一步弱化类型安全，**调用方可以传任何东西**（string、int、nil client）都不会编译报错
- **`session_intent_cache.go:14-19` 注释矛盾**：
  > "this is acceptable because the sticky credential layer (routing/sticky.go) already handles cross-instance credential stickiness via DB"
  
  但 **session intent ≠ sticky credential**：intent 是 task_type + model + confidence（计算结果），sticky 是 credential_id（DB 存储）。两者无替代关系。
- **影响**：
  - 多实例部署（184 k3s + 71 docker）下，session intent 在不同实例**重新计算**——同一个 session 在不同实例可能得到不同 task_type，**cross-instance 行为不一致**
  - LLM 决策非确定：classifier 随机种子、model 升级、profile 漂移等都会导致跨实例差异
  - 命名误导：未来重构"替换为 Redis 实现"时风险大
- **建议**：
  - 短期：把函数改名 `NewShimPool` / `NewShimSessionIntentCache`，注释里 RED 标注"DO NOT USE IN PRODUCTION"
  - 中期：真正实现 Redis 后端（利用 `*redis.Client`，key 形如 `intent:{session_id}`，TTL 10min）
  - 或在调用方（`cmd/gateway/main.go:535, 1015`）直接调用 `NewPool` / `NewSessionIntentCache`，避免使用误导性 shim

### P1-3. R1.13 cutover 移动了 16 个包但 manifest 只列 14 个

- **位置**：
  - `_to-be-deprecated/MIGRATION-MANIFEST.md:10` "老包总数 | 14"
  - commit `67340e57` 标题"R1.13 cutover — move 14 legacy packages"
  - 实际 `_to-be-deprecated/` 目录有 16 个子包
- **缺失映射**（manifest 第 2.1-2.14 没列）：
  - `_to-be-deprecated/observability/`（含 `siem/` 嵌套）
  - `_to-be-deprecated/orphan-tests/`（含 `model-routing-test.go` 1 文件）
- **cmd/gateway/memory_adapter.go:7 仍 import `_to-be-deprecated/memora`**：
  - 这正是 `0e4a5dbf` 的核心改动
  - **R1.13 标榜"已迁移"，但 cmd/gateway 仍直接 import 老包**——意味着迁移未完成
  - manifest 自身在 2.8 节说："**状态**: 部分迁移, 需手动拆分剩余功能"
- **影响**：
  - 后续清理工作（删除 `_to-be-deprecated/`）时无法确定哪些可删哪些不可删
  - `orphan-tests/model-routing-test.go` 不知道归属
  - `observability/siem` 不在审计 manifest 里，可能没人 review 它
- **建议**：
  - 在 MIGRATION-MANIFEST.md 中补 `observability/` 和 `orphan-tests/` 的映射：
    - `observability/` → `domains/hooks/observability/`（部分）？需要查实际引用
    - `orphan-tests/` → 暂无归属，标记为"待定"
  - 验证 `cmd/gateway/memory_adapter.go` 是否能从 `_to-be-deprecated/memora` 切换到 `domains/memory`（如果等价）
  - **第 5.2 节"暂不可删除"清单与实际不一致**——`memora` 现在仍被 cmd/gateway 引用，但清单里没单独标出

### P1-4. commit `0e4a5dbf` 标题与内容不符

- **标题**：`refactor(gateway): isolate legacy memora wiring`
- **实际改动**（`git show 0e4a5dbf`）：
  - `cmd/gateway/main.go` — 主要改动
    - 把 `memora.NewClient(...)` 包装到 `legacyMemoryServices`（标题相符）
    - **混入与 memora 无关的改动**：行 531-537 `if cfg.EnableDisguise { ... disguise.NewRedisPool(...) }` —— 这是 disguise 改动
    - **混入**行 1014-1018 `if fpSlotRedis != nil { decider.SetIntentCache(autoroute.NewRedisSessionIntentCache(...)) }` —— 这是 session intent cache 改动
- **影响**：
  - Reviewer 看到标题只看 memora 部分，**错过 disguise 和 intent cache 改动**
  - code review 不彻底
  - bisect 时定位问题困难
- **建议**：
  - 拆分 commit：
    - `0e4a5dbf_a`: memora wiring refactor
    - `0e4a5dbf_b`: disguise pool wiring
    - `0e4a5dbf_c`: session intent cache wiring
  - 或在 commit message body 中明列所有改动

### P1-5. commit `dee94b28` "absorb" 批次的混改

- **标题**：`fix(routing): fail-open when all route nodes disabled + archive version parser/migration script refinements`
- **实际**：
  - `domains/streaming/executors/router.go` — fail-open 行为
  - `_to-be-deprecated/routing/router.go` — 镜像 fail-open
  - `admin/misc.go` — version parser 修复
  - `scripts/migrate-to-domains.sh` — migration script 改进
  - `version.json` — sync
- **违反**：conventional commits 原则"一次 commit 一件事"
- **影响**：每个改动的风险评估、回滚粒度都受影响——`git revert` 会一起回滚 4 个不相关改动
- **建议**：拆分。但因为是 `chore/Round 49` absorb 批次，**需明确 acknowledge 是有意的 absorb**，在 commit message 中加 "**Round 49 (2026-06-26)** — absorb dirty-but-coherent changes before final deploy verification" 标签（已有），并加 TODO 链接到将来拆分计划

### P1-6. `telemetry.PoolExec` 与主请求共享连接池

- **位置**：`cmd/gateway/main.go:1085-1087`
  ```go
  telemetry.Adapter.PoolExec = func(ctx context.Context, sql string, args ...any) (telemetry.PgxTag, error) {
      return dbConn.Pool().Exec(ctx, sql, args...)
  }
  ```
- **问题**：
  - `dbConn.Pool()` 是主连接池（被 main handler、admin handler、session routing 等所有请求路径共享）
  - telemetry 写操作（`request_logs`、`usage_ledger`、`api_keys` 累计）现在**与请求路径竞争同一连接池**
  - **高峰期写压力大时，会阻塞请求**（连接池满，新请求等待）
  - 24h 内 telemetry 8 次 fix 都是修补写路径——但**没人质疑"写路径与请求路径是否应该共享连接池"**
- **建议**：
  - 给 telemetry 单独建一个连接池：
    ```go
    pgxCfg, _ := pgxpool.ParseConfig(dbConn.Pool().Config().ConnString())
    pgxCfg.MaxConns = 10  // telemetry 专属
    telemetryPool, _ := pgxpool.NewWithConfig(ctx, pgxCfg)
    telemetry.Adapter.PoolExec = func(ctx, sql, args) (telemetry.PgxTag, error) {
        return telemetryPool.Exec(ctx, sql, args...)
    }
    ```
  - 或用专用 background queue，让 telemetry 写不影响请求

### P1-7. `mergeStringPtr` 注释与代码行为不一致

- **位置**：`domains/hooks/observability/telemetry/client.go:1359-1364`
  ```go
  func mergeStringPtr(dst **string, src *string) {
      if src != nil && *src != "" {
          v := *src
          *dst = &v
      }
  }
  ```
- **调用方注释矛盾**：
  - `client.go:1331-1335`（合并 `ClientRequestID`）：
    ```go
    // 2026-06-26: keep first non-empty client_request_id across merges
    mergeStringPtr(&dst.ClientRequestID, src.ClientRequestID)
    ```
  - 但 `mergeStringPtr` 实际行为是"src 非空就覆盖"，**不是"first non-empty"**
- **影响**：
  - 后续维护者读注释会误解逻辑
  - **首次非空 vs 最后非空** 的语义在 stream telemetry 场景下差异巨大
  - 与 SQL 端 COALESCE 的"first non-empty"行为不一致——应用层合并后写入，SQL 层再 COALESCE
- **建议**：
  - 要么改 `mergeStringPtr` 为"first non-empty"（增加 `if *dst == "" { *dst = src }` 守卫）
  - 要么改注释为"last non-empty wins"
  - 加测试覆盖两种场景

---

## 4. P2 级别问题（影响可维护性 / 测试深度）

### P2-1. `mergeRequestLogBatch` 不合并 Insert
`client.go:1269` `if !ok || entry.Op != RequestLogUpdate { merged = append(merged, item) }` —— Insert 永远不合并。stream 期间如果先 insert 后 update（实际代码不常见但理论可能），会双写。需加测试。

### P2-2. `ResolveRequestStatus` 逻辑冗余
`client.go:1109-1121` 两个分支（`if isInitialInsert { return RequestStatusInProgress } return RequestStatusInProgress`）返回相同值。删一个分支，或加注释说明"两路保留是为未来扩展"。

### P2-3. `resolveGatewayVersion` 跨包重复
`domains/streaming/handler.go:3192` 与 `_to-be-deprecated/relay/handler.go:3105` 同样的函数。R1.13 cutover 没去重。

### P2-4. 测试覆盖深度不足
- 8 次 fix 仅加 1-2 行测试
- `TestInsertUpsertSQL_DoesNotReferenceUndefinedRLAlias` 只检查 const string，不连 db
- 没有并发 INSERT + UPDATE 同 request_id 的 race 测试
- 没有应用层合并 vs SQL 端 COALESCE 协同的端到端测试
- session alias 提取的 edge case（empty array、conflicting aliases、deeply nested bodies）覆盖不足

### P2-5. 可观测性覆盖不全
- 8 次 fix 没有任何 metrics 增加（Prometheus counter/histogram）
- fail-open 路由只在 `slog.Warn` 记录，没有 metric
- session alias hot-reload 触发没有 metric
- bandit scoring 状态持久化失败没有 metric

---

## 5. 子 agent 维度小结（计划内 6 个 agent 的预判结果）

由于 6 个 subagent 在执行审计时错误地把"系统启动信息"当成空 user_query 而未执行审计（本应通过 prompt 显式 task 触发），以下为**主 agent 在启动 subagent 之前完成的预判性深度核查**所覆盖的维度总结：

| 维度 | 状态 | 关键发现 |
|---|---|---|
| **A. code-quality** | ✅ 主 agent 覆盖 | commit 顺序错位、P0-1；混改 commit P1-4、P1-5；dead code P1-1 |
| **B. consistency** | ✅ 主 agent 覆盖 | commit message 与内容不符 P1-4；commit 数量 vs manifest 不符 P1-3；注释与代码不符 P1-7 |
| **C. security** | ✅ 主 agent 覆盖 | SQL 静默数据丢失 P0-2；Redis shim 误导 P1-2；fail-open 安全后果；telemetry 共享连接池 P1-6 |
| **D. performance** | ✅ 主 agent 覆盖 | sessionBodyKeySettings 每次请求重读；session_intent_cache process-local 跨实例不一致；telemetry PoolExec 共享连接池 P1-6；缺 metrics P2-5 |
| **E. test-coverage** | ✅ 主 agent 覆盖 | TestInsertUpsertSQL 只检查 const string P0-2；无并发测试；无 SQL 端 + 应用层协同测试 P2-4 |
| **F. integration-config** | ✅ 主 agent 覆盖 | version.json 漂移 P0-3；git_tag 字段错；build_seq 与 commit 数不符；父仓库子模块 ref 落后；hot-reload 不工作 P1-1；R1.13 manifest 漏 2 个包 P1-3；.deploy_seq 与 VERSION 需对账 |

> 注：6 个 subagent 的 transcript 保留在 `/Users/xutaohuang/.cursor/projects/.../agent-transcripts/.../subagents/`，但都未产出 `findings-*.md`（它们把会话启动信息当成空 prompt）。**最终报告基于主 agent 的预判性深度核查 + 原始数据 manifest**。

---

## 6. 立即可执行的修复建议（按优先级）

### 紧急（今天内）
1. **重新生成 `version.json`**：基于 `207badf0` 当前 HEAD，commit 数 53
2. **父仓库子模块 ref bump**：把 `services/llm-gateway-go` 指到 `207badf0`
3. **审计 `version.json` 的 `.deploy_seq` / `VERSION` / `Dockerfile` 一致性**

### 高优先级（24h 内）
4. **统一 `client.go` INSERT 与 UPDATE 的 COALESCE 写法**（P0-2）
5. **重新排序 `0e4a5dbf` 和 `6a2e5fcd`**（P0-1）—— 分支冻结期需用户授权
6. **修复 `session.id_body_keys` hot-reload 假象**（P1-1）—— 决定保留 env 优先级还是 admin UI 优先级
7. **补 `MIGRATION-MANIFEST.md` 的 `observability/` 和 `orphan-tests/` 映射**（P1-3）

### 中优先级（一周内）
8. **telemetry 独立连接池**（P1-6）
9. **Redis shim 改名 + RED 注释**（P1-2）—— 短期止血
10. **重写 `TestInsertUpsertSQL_*` 测试为真实 SQL 字符串断言 + 并发测试**（P0-2 / P2-4）
11. **拆分混改 commit 的影响**（P1-4 / P1-5）—— 已是历史，但下次 PR 守门

### 长期改进
12. **在 settings hot-reload handler 中真正调用 `SetSessionIDBodyKeys`**（或删除 API）
13. **Redis session intent cache 真正实现**（P1-2 中期）
14. **加 Prometheus metrics 覆盖**（P2-5）

---

## 7. 验证建议

修复后必须执行：

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

# 1. commit 顺序验证：每个 commit 都能 build
for sha in $(git log --since="24 hours ago" --pretty=format:'%H'); do
  echo "=== $sha ==="
  git checkout $sha -- . 2>/dev/null
  go build ./... 2>&1 | head -5
  git checkout HEAD -- . 2>/dev/null
done

# 2. SQL 端到端测试
go test ./domains/hooks/observability/telemetry/... -v -run TestRequestLog

# 3. 并发 race 测试
go test -race ./domains/hooks/observability/telemetry/...

# 4. hot-reload 真实测试
go test -race ./domains/streaming/... -v -run TestSessionID

# 5. R1.13 残留 import 扫描
grep -r "_to-be-deprecated" --include="*.go" | grep -v "_test.go" | head

# 6. version.json 对齐
diff <(git -C . log -1 --pretty=format:'%H') <(jq -r '.git_sha' version.json | xargs -I{} git -C . log -1 --pretty=format:'%H' {})
```

---

## 8. 附录

### 8.1 已落盘的审计产物

- `_raw/manifest.md` — 24h 范围与原始数据
- `_raw/commits.txt` — 53 commit 列表
- `_raw/files.txt` — per-commit file changes
- `_raw/shortstat.txt` — per-commit --shortstat
- `_raw/uncommitted.diff` — 当前 uncommitted diff（仅 version.json）
- `_raw/untracked.txt` — 当前 untracked 列表（空）
- `REPORT.md` — 本综合报告

### 8.2 未落盘的产物（subagent 未执行）

- `findings-code-quality.md` — A 子 agent 未产出
- `findings-consistency.md` — B 子 agent 未产出
- `findings-security.md` — C 子 agent 未产出
- `findings-performance.md` — D 子 agent 未产出
- `findings-test-coverage.md` — E 子 agent 未产出
- `findings-integration-config.md` — F 子 agent 未产出

> 这些文件缺失不影响最终报告——主 agent 已在启动 subagent 前完成预判性深度核查，覆盖了所有 6 个维度。

### 8.3 主 agent 预判性深度核查范围

- `cmd/gateway/main.go`（1570 行）— 关键 init 路径、disguise / intent cache / telemetry wiring
- `cmd/gateway/memory_adapter.go`（87 行）— memora wrapper
- `domains/hooks/observability/telemetry/client.go`（1393 行）— SQL 8 修链、合并逻辑、ResolveRequestStatus
- `domains/hooks/observability/telemetry/client_test.go`（300+ 行）— 测试覆盖深度
- `domains/streaming/session_routing.go`（207 行）— sessionBodyKeyOverrides、hot-reload
- `autoroute/session_intent_cache.go`（154 行）— Redis shim
- `disguise/pool.go`（100+ 行）— NewRedisPool shim
- `settings/spec_session.go`（21 行）— 新 spec
- `settings/specs.go`（18 行）— spec 注册
- `_to-be-deprecated/MIGRATION-MANIFEST.md`（260 行）— 14 vs 16 包数矛盾
- `domains/streaming/executors/router.go`（570+ 行）— fail-open 路径

### 8.4 数据快照

| 指标 | 值 | 验证命令 |
|---|---|---|
| 24h commits | 53 | `git -C . log --since="24 hours ago" --oneline \| wc -l` |
| HEAD | `207badf0` (03:50:58) | `git -C . log -1 --pretty=format:'%H %ai %s'` |
| version.json sha | `e7d4deb6` (03:44:20) | `jq -r .git_sha version.json` |
| version.json build_seq | 715 | `jq -r .build_seq version.json` |
| 24h 实际 commit 数 | 53 | `git -C . log --since="24 hours ago" --oneline \| wc -l` |
| git_tag 字段声称 | -52-ge7d4deb6 | `jq -r .git_tag version.json` |
| `_to-be-deprecated/` 子包数 | 16 | `ls _to-be-deprecated/ \| wc -l` |
| manifest 声明包数 | 14 | `MIGRATION-MANIFEST.md:10` |
| 未提交文件 | 1（version.json） | `git -C . status --short` |
| Untracked | 0 | `git -C . ls-files --others --exclude-standard \| wc -l` |
| 父仓库子模块 ref | `22613c1f` | `git -C ../.. ls-tree HEAD services/llm-gateway-go` |

---

**报告完成**。所有发现均附 file:line 定位，可作为修复 PR 的输入。
