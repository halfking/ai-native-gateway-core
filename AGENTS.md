# llm-gateway-go — AGENTS.md（维护者交接）

> 适用：未来任何 cold-start 切到本仓库的助手会话（不区分会话身份）。
> 最近大更新：2026-10-11，host-first 本地部署轮（原 2026-10-09 的「工具链缺失」
> 交接已过时——工具链都在，见下表）。

## 仓库快照（2026-10-11 更新）

- 路径：`/Users/huangxt/workspace/llm-gateway-go-2`（老路径 ~/workspace/llm-gateway-go 已另作他用）
- 远端：codeup.aliyun.com（`origin/main`），多会话共享且推送频繁，提交前先 `git fetch && git status -sb`
- 入口二进制：`cmd/gateway/main.go`（v1，默认 `:8781`）、`cmd/gateway-v2/main.go`（`:8782`，独立）

## 本机工具链实况（2026-10-11 实测；PATH 外的位置要显式加）

| 工具 | 位置 | 要点 |
|---|---|---|
| Go | `~/sdk/go/bin/go`（1.26.0，按 go.mod 自动拉 1.27.1 工具链） | 必须 `GOPROXY=https://goproxy.cn,direct`（proxy.golang.org 超时）；配 `GOMODCACHE=~/sdk/gomodcache GOCACHE=~/sdk/gocache` 复用缓存 |
| PG18+pgcolumnar | `~/kaixuan/pg18`（端口 54318，含 llm_gateway 库） | 客户端在 `~/kaixuan/pg18/bin` |
| Redis | `~/kaixuan/redis`（6379） | 客户端在 `~/kaixuan/redis/bin` |
| netbird | `~/bin/netbird`（daemon 常连；本机 peer nb-macmini-29 = 100.106.169.63） | 控制面 https://netbird.itestu.cn；登录/配置脚本 `~/workspace/myvpn/scripts/netbird-config/` |
| docker | **不存在** | host-first 路径即为无 docker 机器设计 |
| bash | `/bin/bash` 3.2 | 所有脚本必须 bash3 兼容 |

## host-first 本地部署（2026-10-11 落地）

- 方案文档（发现链/自动安装/域名/验证矩阵）：`docs/06-deployment/01-environments/local-host-first-deploy-20261011.md`
- 部署：`LLM_GATEWAY_ROOT=~/kaixuan/llm-gateway-go-v2 bash scripts/deploy-local.sh deploy`（active 8782）
- 密钥在仓库 `.env.local`（gitignored）；缓存=host redis:6379，数据=host pg18:54318
- 域名：`llm.itestu.cn` → NetBird DNS zone `llm-itestu-zone` A → 100.106.169.63:8782
- **坑**：`com.kaixuan.llm-gateway` launchd 代理已停用（备份在 `~/kaixuan/llm-gateway-go-v2/backups/`）——KeepAlive 重生网关会跟 deploy-local 的 nohup+pid 生命周期互踩（抢 8781 候选端口）。要恢复常驻请改造成委托 `deploy start`。
- 契约门：`bash tests/deploy_host_first_contract_test.sh`（verify.sh 已接）；自动安装端到端：`DL_AUTOINSTALL_TEST=1 bash tests/deploy_host_autoinstall_test.sh`（真实源码构建，分钟级，opt-in）

## 迁移通道（apply-db-revision-sequence.sh）2026-10-11 轮修复

host-first 库（只走 startup 通道、Go ensure 链从未启动过）暴露的一批前置缺口已补登（全部幂等；登记位次保证函数链尾为最新体）：**532 / 392 / 631 / 407 / 536 / 540 / 510 / 513 / 516 / 542 / 361b**。结构性修复：新增 **680b**（月视图族按活动 catalog 重建）；**738/740/815/816/817/824** 兼容动态交集形态。两个坑：① PG 的 E'' 替换串里 `\1` 是八进制转义不是 backreference（回引必须普通字符串或全字面量替换）；② 视图族有两代合法形态（冻结 vs 动态交集），通道文件按形态探测分支处理。

## 已知故意红的负向闸门，不要修复也不要加白名单（维护者策略）

- `admin/request_logs_bodies_retirement_gate_test.go` 的 `TestV1BodiesReadersAreAssessed`：故意红，判据是「登记表覆盖全部 bodies 读方」。
- `verify.sh` 的 `go test ./...` 主门长期红是维护者策略；子集验证走 `./admin/... ./bg/... ./domains-stats/... ./settings/...`。

## 关键约束（保留）

- **合并需符合业务逻辑**：避免重复实现功能；保留上游降级契约，使用探针排除的积分源。
- **测试断言需剥离注释后再比对**；按 helper 参数化（不要内联字符串）。
- **部署脚本 macOS timeout 可移植**：禁止裸 `timeout` 调用，必须走 `_dl_timeout` 封装。
- **CGO**：测试/构建用 `CGO_ENABLED=0`。
- **macOS 构建 SDK**：最新 SDK（MacOSX27.0）与 CommandLineTools 链接器不兼容（tapi error）——`dl_resolve_sdkroot` 自动降级（当前可用 26.5）。直接 `go build`/`go run` 不钉 SDKROOT 会链接失败；cgo 形态还会 SIGSEGV。deploy-local 已内置钉扎。
- **依赖安装属用户侧决策**：本助手不做越权安装。
- **SSOT 软链**：`scripts/deploy-lib` → `~/workspace/ai-native-workspace/deploy-lib`。

## 仓库内 `handoff` 命名的语义警告

仓库内 `domains/streaming/handoff_*`、`domains/hostedtask/handoff.go`、`admin/handoff_logs_handler.go`、`settings/handoff_specs.go` 中的 "handoff" 都是**业务流交接**（流式分块确认、托管任务交接、settings 1m 契约测试），不是"会话间交接"；不存在名为 handoff 的 builtin 工具。

## 已落地的回归门

```bash
bash tests/deploy_timeout_portability_test.sh      # timeout 可移植性（verify.sh）
bash tests/deploy_host_first_contract_test.sh      # host-first 发现链（verify.sh）
DL_AUTOINSTALL_TEST=1 bash tests/deploy_host_autoinstall_test.sh   # 自动安装 E2E（opt-in）
bash tests/deploy_local_contract_test.sh           # 仅 SSOT 在场时跑
```
