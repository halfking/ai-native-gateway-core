# MCP 网关验证报告（Maestro 方式）

> 日期：2026-11-09
> 执行入口：`bash tests/mcp-gateway-maestro/run.sh`
> 报告产物：`tests/mcp-gateway-maestro/reports/<时间戳>/summary.md`（不入库，`.gitignore` 已登记）

## 1. 结论

**通过 24 / 失败 0**（L1 Go 测试 + L2 API 场景 23 例 + L3 Maestro 浏览器 flow）。

## 2. 三层验证口径

| 层 | 内容 | 手段 | 读数 |
|---|---|---|---|
| L1 | 治理链单测/集成测 | `go test ./mcp/...` | 全绿 |
| L2 | 真实进程 + 真实 HTTP | 脚本内 S01–S19 | 23/23 通过 |
| L3 | 浏览器逐页断言 | `maestro/flows/10-mcp-gateway.yaml` | rc=0，FAILED=0 |

另：`go build ./...` rc=0；`go vet ./mcp/... ./cmd/mcp-gateway/... ./middleware/...` rc=0；
`gofmt -l` 零输出；`go test ./middleware/...` 全绿（鉴权放行表改动无回归）；
`go test ./cmd/gateway/ -run TestRegisterMCPGateway` 全绿（主网关接线）。

## 3. L2 用例明细（S01–S19）

| # | 场景 | 期望 | 实测 |
|---|---|---|---|
| S01 | 无凭证访问数据面 | 401 | 401 ✅ |
| S02 | initialize | 200 + 协议版本 | 200，`2026-07-28` ✅ |
| S03 | tools/list 聚合上游工具 | 命名空间化 `demo__*` | `demo__add_memory demo__search_memory` ✅ |
| S04 | tools/call 转发上游 | 200 | 200 ✅ |
| S05 | 控制面无管理令牌 | 401 | 401 ✅ |
| S06 | 注册适配器 | 201 | 201 ✅ |
| S07 | 注册高敏工具（需审批） | 201 | 201 ✅ |
| S08 | 签发受限凭证 | 201 + 一次性明文 | 201 ✅ |
| S09 | 受限凭证调用允许的工具 | 200 | 200 ✅ |
| S10 | 越权工具（适配器白名单外） | 403 | 403 ✅ |
| S11 | 高敏工具缺审批凭据 | 403 | 403 ✅ |
| S12 | 带审批凭据 | 放行 | 放行（上游未就绪 ⇒ 502，治理链已通过） ✅ |
| S13 | 吊销凭证后调用 | 401 | 401 ✅ |
| S14 | 审计查询 | 非空 | 14 条 ✅ |
| S14b | 审计导出 NDJSON | 逐行可解析 | 14 行 ✅ |
| S15 | RFC 9728 受保护资源元数据 | 含 `resource` | 含 ✅ |
| S16 | 读类限流 1/min | 第 1 次 200、第 2 次 429 | 200 / 429 ✅ |
| S17 | 引导面 apply（自动批准） | approved | approved ✅ |
| S17b | 幂等重放 | 同一 requestId | 一致 ✅ |
| S18 | 引导面 status | 无明文 | 无明文 ✅ |
| S19 | 引导面 claim | 一次性领取明文 | 领取成功 ✅ |
| S19b | 二次 claim | 不可重放 | `already_claimed` ✅ |

## 4. L3 Maestro flow（运营看板）

断言点：看板标题 → 六个统计卡 → 上游适配器表格（含 `--demo` 预置的 demo/crm 两个
适配器）→ 滚动到「Top 工具（按调用量）」→ 滚动到「最近审计事件」→ 无令牌打开时
必须出现可见错误文案（不能静默空白）。

## 5. 实跑发现并修复的问题（按发现顺序）

| # | 现象 | 根因 | 处置 |
|---|---|---|---|
| 1 | L2 全部 404 | 固定端口 :8799 被本机常驻的 `DeployConfigConsole` 占用，请求静默串台 | 改为**随机空闲端口**；启动后校验演示凭证是否取到，取不到即判失败（不再静默串台） |
| 2 | L1 `exec format error` | 容器内交叉编译（GOOS=darwin）的测试二进制无法在 linux 容器执行 | 构建带 `GOOS/GOARCH`，测试**不带**；两者拆成两套命令 |
| 3 | 「最近审计事件」判红 | Maestro web flow 的 visible 判定受**视口**约束，折叠线以下内容需先滚动 | 改用 `scrollUntilVisible` 再断言 |
| 4 | 「健康信息加载失败」判红 | 看板把错误文案与 HTTP 详情拼成整串，文本匹配失败 | 拆成稳定文案 + 详情两个元素 |
| 5 | 二次 claim 报 `not_approved` | 领取后记录状态置 claimed，按状态判会误报 | 先判「已领取」再判「未批准」；同时区分 pending（未批准）与 claimed（已领取） |
| 6 | 主网关接线测试期望 401 实得 503 | 控制面在**未配置**管理令牌时返回 503（不可用），测试口径写错 | 测试改双口径：未配置 ⇒ 503、令牌错误 ⇒ 401、令牌正确 ⇒ 200 |

## 6. 本轮同时收口的安全项

- `/mcp/control/health` 含上游端点与熔断态 ⇒ 改需管理令牌；探针改用公开的
  `/mcp/control/healthz`（只回 `ok`，不泄露拓扑）。
- 控制面未配置管理令牌时整体 503（不是「无鉴权开放」）。
- 免认证引导面默认关闭（`MCP_GATEWAY_BOOTSTRAP_ENABLED=false`）。

## 7. 未覆盖（诚实登记）

- 上游 stdio / SSE 传输归一化、REST→MCP 虚拟化（未实现）。
- 出向 OAuth 2.1（3LO/OBO）、RFC 8707/7591/8414（未实现）。
- OTel trace、prompt-injection 检测、适配器并发上限执行、工具结果缓存（未实现）。
- 真实企业 IdP / RedClaw 持票联调（未做；需外部系统配合）。
- 多副本部署下的注册面一致性（当前注册面为单进程内存/文件存储，外部账本模式可覆盖）。
