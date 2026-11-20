# 子审计 C：maas 列车（2026-11-18 后 48h 窗口）· 主控自查版

> 原计划派子代理，因配额中断后由主控直接自查关键判定。完整逐文件审计（幂等重放/租户边界/契约对齐细节）留待补充；本报告覆盖最高价值判定：**接线面（死面判定）**。

## 一、结论总表

| # | 审计问题 | 判定 | 一句话结论 |
|---|---|---|---|
| 1 | maas HTTP API 接线进网关主程序 | **未实现（死面）** | `api/maas`（maasapi 包）全仓零 import；`maas` 领域包已接线（main.go:3331/6476，内部月账 worker 用）但 REST 边界没 wire 到 mux —— 与 main.go:3630 注释自记的 output-compliance 同族病 |
| 2 | 领域包实现规模 | 已实现 | `maas/` 49 个 Go 文件（bills/campaigns/coupons/credits/ledger 等）+ `contracts/maascontracts/` 10 文件契约包 |
| 3 | HTTP 层实现 | 已实现（未达产） | `api/maas/` 10 端点 + 幂等键强制 + 错误映射 403/409/422/503 + Prometheus/OTel（`3e58e6c64`/`3f2cd36da`）+ server_test/metrics_test/tracing_test |
| 4 | 幂等重放真实现 | ⏳ 待深查 | `b63ddf537` 自称修复；HTTP 层有 Idempotency-Key 强制校验（8-128 字符）；领域层重放逻辑未逐文件核 |
| 5 | schema/迁移 | ⏳ 待深查 | 未见 maas 专属迁移登记（990/991/981/980/992 均非 maas）；MAAS 表若在 990 内则登记面 OK（子审计 B 已核 990 七面） |

## 二、关键证据（问题 1）

- `grep -rln "kaixuan/llm-gateway-go/api/maas" --include="*.go"` **除 api/maas 自身外零命中** ⇒ 无任何调用方。
- `api/maas/server.go:27 NewServer` 存在但无人构造。
- 领域包 `maas` 的两个生产接线点：`cmd/gateway/main.go:3331`（`maasSvc := maas.NewService(dbConn.Pool())`）与 `:6476`（MonthlyBillWorker，24h 周期）——**内部记账面活**，对外 REST 面死。
- 仓内同族先例（`cmd/gateway/main.go:3630-3634` 注释原话）：「handler 写好了、RegisterRoutes 也有，但从来没被 wire 到 main mux，于是页面所有 API 全 404……它的单测能过是因为测试自己 new 了一个 handler 直接打，覆盖不到『路由到底注册没有』这一层。」
- 提交链自述（`f717e9554` 文档「10 端点 + 部署」、`3e58e6c64`「REST 传输边界」）与「零接线」现状不符 ⇒ **文档自述超前于实现**。

## 三、缺陷清单

| 级别 | 编号 | 缺陷 | 处置建议 |
|---|---|---|---|
| P2 | C-1 | `api/maas` 10 端点零消费者（死面）；文档已按「可部署」口径写（v0.1 + 部署章节）⇒ 读者会以为 API 可用 | 二选一：① wire 进 main mux（加路由注册 + 端点可达钉测——照 output-compliance 教训补「路由注册测试」）；② 在 api/maas/README.md 头部显式标注「未接线，Phase X 接入」防误用 |
| P3 | C-2 | 幂等重放/租户边界/契约对齐未逐文件复核（主控自查范围有限） | 下一班触达 maas 时按原提纲补齐 |

## 四、登记建议

1. C-1 归 maas 列车后续轮（接线属产品节奏裁决：maas 对外 API 何时开放）。
2. 若接线：必须带「路由注册存在性」钉测（本仓 §708①/§3633 两代教训）。
