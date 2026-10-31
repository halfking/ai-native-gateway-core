# 2026-10-27 r1039 后续轮：Echo 侧会话吊销对齐 + 跨 checkout 切流互斥

> 起点档：`docs/audit/2026-10-20-r1039-脱敏阻断与设置界面审计轮.md`（r1039，脱敏 / 阻断 / 设置界面）
> 基线：`11ce6add8`（main @ origin/main）
> 本轮范围：r1039 审计档 **§8 遗留项**里**不需要 owner 裁决**、可直接落地的那两条
> 本轮产出：本文件 + `cmd/gateway/r1039_jwt_middleware_revocation_test.go`
> + `tests/deploy_switch_lock_contract_test.sh`

---

## 0. 结论速览

| # | 事项 | 结论 |
|---|---|---|
| 1 | §8 遗留项逐条复核 | 18 条里 **13 条已闭合 / 1 条已被后续轮解决 / 4 条需 owner 或产品裁决**；**2 条**属「工程可执行、无需裁决」⇒ 本轮做这两条 |
| 2 | **Item A** §8 第 9 条：Echo 侧没有「改密即吊销」探测 | **已修**：Echo 侧接上**与 ServeMux 侧同一份**探测实现（`admin.RevokedByPasswordChange`）⇒ 改密后旧会话在 Echo 的 `/api/admin/*` 上**当场 401**。判据 6 条 + 变异臂 **M1~M4 全红** |
| 3 | **Item B** §8 第 18 条：跨 checkout 切流互斥 | **已修**：机器级 `switch` 锁覆盖「候选起停 → 切流完成」，争用**快速失败**（rc 75）而不是抢端口。判据 6 条不变量 + 变异臂 **N1~N4 全红** |
| 4 | 安全边界 | Item A **只做「对齐」**：不新增、不放宽任何撤销条件；读失败一律放行（fail-open）⇒ **不会**把管理面锁死 |
| 5 | 零 DDL | 本轮**不产生任何 SQL** ⇒ 211 / 252 无需为本轮代码应用迁移 |
| 6 | 既有红灯 | `cmd/gateway` 的 **20 条 lite 存储模式用例**仍是既有红（需 CGO；pristine `origin/main` 同样红）⇒ 本轮**零新增红** |

---

## 1. 起点：r1039 审计档 §8 的 18 条遗留项，今天还剩什么

r1039 那一轮自己就完成了 §7.13 / §7.14 两个自审轮（混拼标记漏检 + 探针路径缺内层重扫），
并在 §8 里登记了 18 条遗留项。**本轮先逐条核对当前树**，再决定动哪几条 —— 免得重复排查。

| §8 # | 事项 | 本轮核对结果（`11ce6add8`） | 处置 |
|---|---|---|---|
| 1 | 【裁决】egress 默认档 | 默认仍是 `{input:"*"}`（`domains/sanitizerules/hardblock.go`）⇒ **仍需 owner 拍板** | 不动 |
| 2 | 【裁决】`pii.address` 误伤「订单号/工单号」 | YAML 规则仍是 `[^\s]{2,}[省市区县镇乡村路街道巷弄号栋单元室]\s*[\d\-号栋单元室]+`；但 **12h 审计轮**已修掉同族更重的一条（真地址明文外泄，`r1039_address_before_precise_value_test.go`）⇒ 「前导『号』」这一条**仍冻结** | 不动（裁决项） |
| 3 | 【设计】占位符零熵 + 整会话映射 | 仍需一次设计讨论（收敛点在外发面） | 不动（设计项） |
| 4 | 【环境】本机上游凭据耗尽 | 环境事实 | 不动 |
| 5 | 【环境】request_logs 写入 | 已订正为「写侧是通的」 | 已闭合 |
| 6 | 【口径】`schema_migrations` 不是权威台账 | 口径提示 | 已闭合（文档） |
| 7 | 【工具】FilePanel Browser 不可用 | 工具事实（验收走无头 Chrome + CDP） | 已闭合（文档） |
| 8 | 占位符标记词混拼漏检 | **r1039 审计档 §7.13 已修**（该文件已划掉） | 已闭合 |
| 9 | **【既有差异】Echo 侧没有「改密即撤销会话」的探测** | **仍成立**：ServeMux 侧 4 个中间件都带探测（`admin/auth.go:112/424/484`、`admin/provider_access.go:49`），Echo 侧**没有** | ★ **本轮 Item A** |
| 10 | 【裁决】`/admin/usage-enhanced` 菜单归属 | 仍是 `navCoverage.test.ts` 的 `KNOWN_ORPHANS` | 不动（产品决策） |
| 11 | 【裁决】v1 族历史数据归档还是丢 | 890 的非空闸仍在 | 不动（owner 决策） |
| 12 | hot-cron 盲调已删的 promote 函数 | **上游已修**（能力探针，§7.10 实测不再出现 42883） | 已闭合 |
| 13 | auto-index rollup 引用已改形状的视图列 | **上游已修**（§7.10 实测不再出现 42703） | 已闭合 |
| 14 | 【环境】`.env.local` 写侧闸 | 本机已补 V2-only 形态 | 已闭合 |
| 15 | 【工具·上游】`TestLogAudioUsageStopWriteGate` 失效 | **该用例在当前树里已不存在**（`grep` 零命中）⇒ 后续轮已处理 | 已闭合 |
| 16 | 【覆盖】动态路由覆盖缺口（7 条取不到实参） | 仍需造业务数据（**写操作**） | 不动（需写操作授权） |
| 17 | 【语义】`request-journeys` 列表 vs 详情不一致 | 仍需产品决策（给「在途」一个明确态） | 不动（产品决策） |
| 18 | **【环境·并发】跨 checkout 部署互斥** | **仍成立且可执行**：机器级构建锁（`kx-llm-gateway-build.lock`）**跨 checkout 共享**，但它在**候选实例起停之前**就被释放 ⇒ **切流窗口无保护** | ★ **本轮 Item B** |

⇒ **18 条里，本轮该动的是 2 条**：#9 与 #18。两条都**不需要 owner 裁决**（#9 是「对齐既有策略」，
不是「改策略」；#18 是纯工程），其余要么已闭合，要么需要裁决 / 产品决策 / 写操作授权。

---

## 2. Item A（§8 第 9 条）：Echo 侧「改密即吊销」对齐

### 2.1 缺陷本体：同一套账号体系，换个 HTTP 框架就绕过了改密吊销

「改密即吊销」这条策略**只有一半存在**：

| 侧 | 中间件 | 有没有「改密吊销探测」 |
|---|---|---|
| ServeMux | `admin.AdminMiddleware`（`admin/auth.go:112`） | ✅ 有 |
| ServeMux | `admin.tenantAdminOrSuperAdmin`（`admin/auth.go:424`） | ✅ 有 |
| ServeMux | `admin.SuperAdminMiddleware`（`admin/auth.go:484`） | ✅ 有 |
| ServeMux | `admin/provider_access.go:49` | ✅ 有 |
| **Echo** | `newJWTMiddleware`（`cmd/gateway/main_jwt_middleware.go`） | ❌ **没有** |

⇒ **改密之后**，用旧 cookie / 旧 Bearer token 的会话：
- 在 ServeMux 侧端点 ⇒ **当场 401**；
- 在 Echo 注册的 `/api/admin/*` 端点 ⇒ **继续全权可用**。

Echo 侧的实际影响面（r1039 那一轮逐页验收实测过 cookie-only 可达的那批）：

```
/api/admin/tiers          /api/admin/licenses        /api/admin/vibecoding/*
/api/admin/faults         /api/admin/releases        /api/admin/autoupdate
/api/admin/center/*       /api/admin/routing-opt/ml
```

机制本身在 `admin/auth_revocation.go`：`settings_kv` 的 `admin.auth_revocations` 键存
每个用户的**改密纪元**（RFC3339），认证中间件比对「token 签发时间 < 纪元」⇒ 401；
读失败一律**放行**（fail-open），走 5s 进程内 TTL 缓存。

### 2.2 修法：**只做对齐**，不新增策略

1. `admin/auth_revocation.go`：新增导出外壳
   ```go
   func RevokedByPasswordChange(ctx context.Context, db *pgxpool.Pool, userID int, issuedAt time.Time) bool {
       return authRevocationProbe(ctx, db, userID, issuedAt)
   }
   ```
   ⇒ 两边**共用同一个变量、同一份缓存、同一条 fail-open 尺子** ⇒ 口径不会漂。
2. `cmd/gateway/main_jwt_middleware.go`：
   - `newJWTMiddleware(secret string, db *pgxpool.Pool)`（多一个连接池参数）；
   - 验签之后、注入身份之前，做一次探测；命中 ⇒ `401 {"error":"session revoked: password changed"}`；
   - 探针走**测试缝** `var jwtRevocationProbe = admin.RevokedByPasswordChange`（与 `admin` 侧
     `authRevocationProbe` 同一形态）。
3. `cmd/gateway/main.go`：把 `dbConn.Pool()` 交给中间件（`Pool()` 对 nil / 已关闭都安全 ⇒
   **没有数据库时探测 fail-open**）。

**安全边界（写在这里，不是藏在提交信息里）**：

- ✅ **只判「签发时间早于该用户的改密纪元」这一条** ⇒ 不新增、不放宽任何撤销条件；
- ✅ 读不到 `settings_kv` 一律**放行** ⇒ 设置库不可用**不会**把管理面锁死在 401；
- ✅ Echo 侧用的是**本地签发器**（`admin.VerifyToken`）⇒ 没有跨项目 principal 的 user_id
  语义问题 ⇒ 与 ServeMux 侧对 legacy principal 的处理一致（那边对 multi_issuer principal
  跳过本探测）；
- ⚠ **响应体形状按各自传输的既有约定**：ServeMux 侧是 `{"error":{"detail":"…"}}`
  （`admin.writeError`），Echo 侧是 `{"error":"…"}`（与同一中间件里其它 401 一致）。
  ⇒ **提示文案逐字相同**（`session revoked: password changed`），形状差异是**既有约定差**，
  本轮**不改**（改它要动一整套 Echo 错误体契约）。

### 2.3 判据（`cmd/gateway/r1039_jwt_middleware_revocation_test.go`，6 条）

| # | 用例 | 钉住什么 |
|---|---|---|
| 1 | `RevokesBearerAfterPasswordChange` | 改密**前**签发的 Bearer ⇒ 401 + 正文逐字含 `session revoked: password changed` + **不得到达 handler** |
| 2 | `RevokesCookieSessionAfterPasswordChange` | 同上，但走**浏览器那条路**（HttpOnly cookie）⇒ 只钉 Bearer 的话「cookie 路径漏掉」照样全绿 |
| 3 | `KeepsSessionIssuedAfterPasswordChange` | **反向对照**：改密**后**签发的会话照旧可用 ⇒ 没有这一条，「一律 401」也能让 1/2 全绿 |
| 4 | `RevocationProbeSeesTokenClaims` | 探针拿到的 user_id 与**签发时间**必须来自 token claims（杀掉「传 `time.Now()`」「传零值」两种接线错误） |
| 5 | `RevocationFailsOpenWithoutDatabase` | 没有数据库 ⇒ **放行**，不 401、不 panic |
| 6 | `RevocationDoesNotShadowCredentialErrors` | 凭据本身坏掉时，401 正文必须是 `invalid or expired token`，**不得**被改密那句覆盖（运维分不清「会话过期」与「改密踢出」就会查错方向） |

### 2.4 变异臂（M1~M4，**4/4 断言红**，非编译红）

| # | 变异 | 打红的判据 | 结果 |
|---|---|---|---|
| M1 | 摘掉探针调用（回到修复前形态，用 `false &&` 短路以保持可编译） | 1 / 2 / 4 | **红** |
| M2 | 吊销分支 401 → 403 | 1 / 2 | **红** |
| M3 | 签发时间改传 `time.Now()` | 1 / 2 / 4 | **红** |
| M4 | 签发时间改传零值 | 3 / 4 | **红** |

⇒ 四臂还原后 `cmd/gateway/main_jwt_middleware.go` **md5 逐字一致**。

### 2.5 本条**明确没做**（留给下一轮，别重复排查）

- **`MustChangePassword` 闸**：ServeMux 侧 `AdminMiddleware` 还有一道「必须改密的账号只能访问改密路径」
  的闸（`enforceMustChangePassword`），Echo 侧**没有** ⇒ 这是一道**既有差异**，但属**安全策略变更**
  （前端在 Echo 端点上收到 403 后的行为需要单独确认）⇒ 本轮**刻意不扩大**改动面。
- **校验器统一**：ServeMux 的 `AdminMiddleware` 走 `identity.Verify`（多签发者 / 共享 principal），
  Echo 侧走 `admin.VerifyToken`（本地签发器）⇒ 本轮**只接吊销探测**，不动校验器
  （换校验器会改变「接受哪些 principal」，属另一个量级的改动）。

---

## 3. Item B（§8 第 18 条）：跨 checkout 切流互斥

### 3.1 缺陷本体：端口是机器级资源，锁却只覆盖「同一部署根」

本机有多份 checkout（`llm-gateway-go` / `-3` / `-5` / `-cursor`），它们**共用同一个宿主端口对**
（8781 / 8782），而部署脚本的蓝绿切换是**固定 8781/8782 一对**的 ⇒ 两份 checkout 同时部署时，
**切流步会互相踩**。r1039 审计档 §7.14.7 记着一次实测：

```
候选在 8781 起好、验证全绿（含版本一致性 + 凭据解密冒烟），
切流步要抢占 8782 ⇒ 那里跑着**另一个部署根**的进程
（两个根各有自己的 pid/run 目录 ⇒ 本轮的 stop_instance 管不到它）
⇒ 新实例绑不上 8782 ⇒ 老进程继续应答 /version（报 2876）
⇒ 「/version body did not match bundle metadata」⇒ 重试 3 次全红 ⇒ **回滚**
```

⇒ 根因不是「端口漂移」，是**互斥的作用域搞错了**：锁管得住「同一根的两个部署」，
**管不住「两个根」**。

### 3.2 为什么「已有的构建锁」不够

机器级构建锁 `kx-llm-gateway-build.lock`（`LOCK_TMP_ROOT=/tmp`）确实**跨 checkout 共享**
—— 但它在 **候选实例起停之前**就被释放了：

```bash
bundle=$(stage_release "$binary")
release_build_lock          # ← 构建锁在这里就交还了
...
start_instance "$bundle" "$candidate_port"   # ← 切流窗口从这里开始，全程无锁
...
dl_atomic_switch "$RELEASE_VERSION"
```

⇒ 构建锁只保护「checkout 的版本号 / web/dist 变更」这件事（这是它本来的职责），
**覆盖不到切流窗口**。

### 3.3 修法：一把**机器级**的切流锁

`scripts/deploy-local-lib.sh` 新增原语（**自包含**，不依赖仓外的共享部署库）：

| 函数 | 语义 |
|---|---|
| `dl_switch_lock_path()` | `${LOCK_TMP_ROOT:-/tmp}/kx-llm-gateway-switch.lock` ⇒ **机器级**，**不含任何 checkout 路径** |
| `dl_acquire_switch_lock()` | flock（有 flock 且 bash ≥ 4.1）/ mkdir 回退；争用 ⇒ **rc 75**（EX_TEMPFAIL）+ 打印持有者元数据（pid / started_at / commit / version） |
| `dl_release_switch_lock()` | 释放；未持有时是 no-op |

`scripts/deploy-local.sh` 的接线：

| 位置 | 动作 |
|---|---|
| `deploy()` 里 `release_build_lock` **之后**、`start_instance "$bundle" "$candidate_port"` **之前** | `dl_acquire_switch_lock \|\| die 'another deployment is switching the active port; retry after it finishes'` |
| `deploy()` 末尾（`dl_atomic_switch` + 记账 + `record_success` 之后） | `release_switch_lock` |
| `cleanup_deploy()`（EXIT / INT / TERM trap） | `release_switch_lock` ⇒ 夭折的部署**不会**把锁留成永久占用 |

**刻意不做的**（与共享部署库 `lock.sh` 的既有语义保持一致）：

- **不按「年龄」自动清理残锁** —— 部署可能正在长时间跑，按年龄删锁会打断别人；
  元数据里有 pid，运维核对后手工删除；
- **不改动仓外的共享部署库 SSOT**（`~/workspace/ai-native-workspace/deploy-lib/`）⇒
  本轮改动**全部落在本仓**（`scripts/deploy-local*.sh`），可提交、可评审。

### 3.4 判据（`tests/deploy_switch_lock_contract_test.sh`，6 条不变量）

| # | 不变量 | 判据形式 |
|---|---|---|
| I1 | 切流锁是**机器级**的 | 路径必须**逐字**等于 `${LOCK_TMP_ROOT}/kx-llm-gateway-switch.lock`；且**换 cwd 再算一次必须一样**（⇒ 路径与 checkout 无关） |
| I2 | 争用**快速失败** + 打印持有者元数据 | 第二次获取必须返回 **75**，且元数据里有 `pid=` / `started_at=` |
| I3 | 释放后可再次获取 | 释放 ⇒ 再获取 ⇒ rc 0（锁不会变成死结） |
| I4 | 持锁窗口**覆盖候选起跑** | 源码顺序：`dl_acquire_switch_lock` 的行号 **<** `start_instance "$bundle" "$candidate_port"` 的行号 |
| I5 | 释放点在**切流完成之后** | 源码顺序：`dl_atomic_switch "$RELEASE_VERSION"` 的行号 **<** 最后一次 `release_switch_lock` 的行号 |
| I6 | **失败路径也释放** | `cleanup_deploy()` 函数体里必须有 `release_switch_lock` |

判据纪律：

- 行为判据（I1~I3）跑**真函数**，用 `LOCK_TMP_ROOT` 指向临时目录 ⇒ **不碰机器上真实的锁**，
  也不碰任何正在跑的部署；
- 顺序判据（I4~I6）断言的是「**谁在谁前面**」，不是「某个字符串出现过」
  ⇒ 出现过 ≠ 承重。

### 3.5 变异臂（N1~N4，**4/4 断言红**）

| # | 变异 | 打红的判据 | 结果 |
|---|---|---|---|
| N1 | 把获取切流锁挪到候选实例起跑**之后** | I4（acquire=1816 > candidate=1815） | **红** |
| N2 | `cleanup_deploy` 不再释放切流锁 | I6 | **红** |
| N3 | 把锁路径改成 checkout 相关（`…-$(basename $PWD).lock`） | I1 | **红** |
| N4 | 把释放挪到切流**之前** | I5（release=1873 < switch=1874） | **红** |

⇒ 四臂还原后 `scripts/deploy-local.sh` 与 `scripts/deploy-local-lib.sh` **md5 逐字一致**。

⚠ **一处自证**：N3 的**第一版**替换体因为 shell 引号嵌套写坏，落成了一个**垃圾路径**
（`…-20 20 12 61 79 80 …basename ).lock`）⇒ 门照样红了，但**红的理由不是「路径变成 checkout 相关」**
⇒ 按仓内纪律「变异本身也要先自证它确实改到了目标」，用 python3 重做了一遍，
确认落点是 `…-llm-gateway-go-3.lock`（**真·checkout 相关路径**）⇒ 仍红。

### 3.6 本条**明确没做**（留给下一轮，别重复排查）

- **§8 第 18 条的另一半**（git 侧）：兄弟 checkout 会把 `main` 推走、能把别人的提交从
  `origin/main` 上挤掉 ⇒ 那需要**流程约定**（每轮工作放独立分支、由一个 owner 合并），
  代码管不了 ⇒ 本轮只在文档里登记，**没改** `AGENTS.md` 的并发纪律段
  （本机有并行会话在编辑同一份 `AGENTS.md`，避免冲突）。
- **部署根级别的端口所有权登记**（哪个根拥有 8781/8782）：本轮的锁解决的是
  「两个部署不许同时切流」，**没有**解决「谁拥有这个端口」⇒ 仍是运维事实。
- **远程部署（154 / 245）侧的同类互斥**：共享部署库 `lock.sh` 的 `lock_acquire_remote`
  已按 target 隔离 ⇒ 本轮**没动**（也不该在本仓动仓外 SSOT）。

---

## 4. 读数与验证

| 项 | 读数 |
|---|---|
| `go build ./...` | ✅ 干净（`CGO_ENABLED=0`，go 1.27.2 工具链） |
| `go vet ./cmd/gateway/ ./admin/` | ✅ 干净 |
| `go test ./cmd/gateway/ -run 'TestR1039*'` | ✅ 全绿（含既有 cookie 用例 + 本轮 6 条） |
| `go test ./admin/` | ✅ 全绿（136.4s） |
| `go test ./scripts/ursmcheck/`（**整包**） | ✅ **ok（1240.9s）** —— ★ 跑满 20.7 分钟是**机器争用**（本机 load 一度 ~99，兄弟会话并跑 `go test ./...`），不是本轮改动慢；⇒ 按仓内既有处方用 `-timeout 40m` 跑（默认 10m 会被高负载打断，r1039 审计档 §10 已登记同一形态）⇒ **包内零红**（含本轮一度弄红的 admin 路由快照那条） |
| `bash tests/deploy_switch_lock_contract_test.sh` | ✅ ALL PASS（6 条不变量） |
| `bash tests/deploy_local_contract_test.sh` | ⚠️ 见下方「既有环境性 flake」：本轮改动**不**让任何既有断言变红，但它自身有一条 Redis 发现断言**时绿时红**（既有，非本轮引入） |
| `bash tests/deploy_blue_green_contract_test.sh` | ✅ OK（4 测） |
| 变异臂 | Go 侧 **M1~M4 4/4 断言红**；shell 侧 **N1~N4 4/4 断言红**；还原后 md5 逐字一致 |
| SQL | **零 DDL** ⇒ 211 / 252 无需为本轮代码应用迁移 |

⚠ **一条【既有】环境性 flake（零因果对拍已做）** —— `tests/deploy_local_contract_test.sh` 的
Redis 发现断言**时绿时红**（本轮实测：本树 6 次里红 4 次、2 次绿）。
**零因果对拍**：在**不含本轮提交**的 pristine worktree（`e936c50b0` = 本轮合并的
**另一父**）上跑同一条测试 ⇒ **6 次全红**（5 次红在「named Redis discovery should pick
nbjl-redis」、1 次红在同族的「scan-without-redis-cli should accept by port heuristic」）
⇒ **与本轮改动零因果**（本轮改的是切流锁原语 + Echo 中间件，**不碰** Redis 发现路径）。
⇒ 登记为**既有环境性 flake**，本轮**不修**（要修得先弄清「mock 的 `docker ps`
为什么偶发不生效」，属另一条工作线）。

⚠ **一条由本轮提交引入、并已由上游修掉的门红（诚实记账）** —— 本轮给
`cmd/gateway/main.go` 加了 **3 行**（注释 + 改调用）⇒ `scripts/audit/asset/admin-routes.txt`
快照里 **106 处 `file:line` 全部前移** ⇒ 快照与源码脱钩 ⇒
`scripts/ursmcheck` 的 `TestAdminRoutesSnapshotMatchesGenerator` 红。
⇒ **不是本轮判据抓到的，是并行会话在 `go test ./scripts/ursmcheck/` 全量跑时发现的**
⇒ 已由上游提交 **`3693353b0`**「刷新 admin 路由清单快照 —— 修 r1039 提交留下的行号漂移」
用**生成器的正规更新模式**修好（`bash scripts/audit/extract-admin-routes.sh --update-snapshot`
⇒ 106 行差异，去掉行号后**逐行比对差异 0** ⇒ 纯行号漂移、零路径/方法变化）。
⇒ **本轮在这条修好之后的树（`origin/main` = `3693353b0`）上复跑该门：`ok`（16.2s）**。

⚠ **既有红灯（不是本轮引入）**：`cmd/gateway` 的 **20 条 lite 存储模式用例**
（`TestLiteRequestLogSink_*` / `TestLiteCatalog*` / `TestInitStorageMode*` …）
在 macOS 上需要 CGO SQLite 驱动，而本仓在 macOS 上必须 `CGO_ENABLED=0` 构建
⇒ pristine `origin/main` **同样红、同名同因**（r1039 审计档 §10 已登记）⇒ 本轮**零新增红**。

---

## 5. 本轮明确没做（留给下一轮，别重复排查）

- **§8 第 1 条**（egress 默认档）与 **§8 第 2 条**（`pii.address` 前导「号」）：**owner 裁决项**；
- **§8 第 3 条**（占位符零熵 + 整会话映射）：需要一次**设计讨论**（收敛点在外发面）；
- **§8 第 10 / 17 条**（`/admin/usage-enhanced` 菜单归属、journey「在途」态）：**产品决策**；
- **§8 第 16 条**（7 条动态路由覆盖缺口）：要**造业务数据**（写操作）；
- **Echo 侧的 `MustChangePassword` 闸** 与 **校验器统一**（见 §2.5）；
- **§8 第 18 条的 git 侧那一半**（分支纪律 / owner 合并约定，见 §3.6）；
- **r1039 审计档里那批 i18n / Web UI 线的遗留**（第 9~14 增量已登记的
  `t(k)` 形态引用集盲区、页面级硬编码中文、语言包死键、字表正名单、移动端 8 语言）
  ⇒ 与本轮**不同线**，别混。

---

## 6. 提交与推送

- 分支：`main`（与 origin/main 同步，基线 `11ce6add8`）；
- 提交范围 **10 个文件**：

  | 文件 | 性质 |
  |---|---|
  | `admin/auth_revocation.go` | 源码：导出 `RevokedByPasswordChange` |
  | `cmd/gateway/main_jwt_middleware.go` | 源码：Echo 侧接上吊销探测 |
  | `cmd/gateway/main.go` | 源码：把连接池交给中间件 |
  | `cmd/gateway/r1039_jwt_middleware_cookie_test.go` | 既有判据：签名跟着改（2 处调用点） |
  | `cmd/gateway/r1039_jwt_middleware_revocation_test.go` | **新判据**：6 条 |
  | `scripts/deploy-local-lib.sh` | 源码：切流锁原语 |
  | `scripts/deploy-local.sh` | 源码：切流锁接线（acquire / release / trap） |
  | `tests/deploy_switch_lock_contract_test.sh` | **新契约测试**：6 条不变量 |
  | `docs/audit/2026-10-27-r1039-后续轮-…md` | 本文件 |
  | `LESSONS.md` | **§476**（上游已占 §473/§474/§475 ⇒ 本轮改号） |

- 提交：
  - `a0609e4d1` — 本轮改动（10 个文件，含 §476）；
  - `f17fe4c1a` — 合并上游 10 个提交（冲突只在 `LESSONS.md`，**两侧都保留**；
    上游已占 §473/§474/§475 ⇒ 本轮让号改 **§476**）；
  - `origin/main` 后续由并行会话推进到 `3693353b0`（含 §4 记的那条快照修复）。
- 推送：`origin/main`；回验 `a0609e4d1` 仍是 `origin/main` 的祖先。
- ★ **本轮留下的两处「诚实记账」**（都写进了 §4）：
  ① 本轮给 `cmd/gateway/main.go` 加 3 行 ⇒ admin 路由快照的行号漂移
     ⇒ `scripts/ursmcheck` 一条门红 ⇒ **已由上游 `3693353b0` 用生成器正规模式修好**；
  ② `tests/deploy_local_contract_test.sh` 自身的 Redis 发现断言**时绿时红**
     ⇒ **pristine（不含本轮提交）6/6 全红** ⇒ 既有环境性 flake，零因果。
