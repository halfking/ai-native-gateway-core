# Owner 确认清单：清理候选 C1-C5 + S4 provenance 缺口（2026-09-24）

> 来源：R59 轮 §3.3 第 5/10 项、R60 遗留收口轮待办、R61 轮 §六.9。
> 用途：以下各项**代码面核实已完成**（import 级零引用/缺口定位），删除与否属于产品/架构
> 裁决，不属于审计轮可自决范围——逐项列决策请求，owner 勾选后由独立清理轮执行。
> 核实基线：HEAD = c3217c9c5（2026-09-24）；C1-C4 零引用已于本日 grep 复核仍成立。

## 一、清理候选（删除裁决，C1-C5）

| # | 候选 | 范围 | 核实状态（c3217c9c5） | 删除影响 | owner 裁决 |
|---|---|---|---|---|---|
| C1 | `domains/session/preprocess` | 整个包 | import 级全仓零引用（grep 复核 09-24 仍零） | 无生产编译影响；git 历史可恢复 | ☐ 删除 ☐ 保留（理由：____） |
| C2 | `adapter/unified` | 整个包 | import 级全仓零引用（同上） | 同上 | ☐ 删除 ☐ 保留（理由：____） |
| C3 | `internal/fsstore` | 整个包 | import 级全仓零引用（同上） | 同上 | ☐ 删除 ☐ 保留（理由：____） |
| C4 | `domains/nodestatecache` | 整个包 | import 级全仓零引用（同上） | 同上 | ☐ 删除 ☐ 保留（理由：____） |
| C5 | WeightedRouter 集群 | `domains/routing/weighted_router.go` + `minheap_topk.go` + 各自 test | 生产零调用；唯一外部引用 = `tests/local/gateway/main.go`（local harness） | 随 tests/local harness 一起退役；routing 包内 3 文件+2 测试删除 | ☐ 删除（含 harness 退役） ☐ 保留（理由：____） |

## 二、S4 provenance 缺口（建设 vs 放弃裁决，非删除）

| # | 缺口 | 现状 | 选项 | owner 裁决 |
|---|---|---|---|---|
| P-1 | `sanitize_map_ref` 无 request 链路生产者（V2 路径 2.5/3 层） | 读侧白名单齐全但全仓无生产写入点；读侧代码空转 | ☐ 补生产者（V2 metadata 写入 sanitize_map_ref，补齐三层 provenance 闭环） ☐ 放弃并删读侧白名单（承认两层足够） ☐ 暂缓（理由：____） |
| P-2 | threetier 校验包零外部导入 | 校验逻辑完备但无调用方 | ☐ 接入（在 V2 写路径挂校验） ☐ 删除 ☐ 保留为离线校验工具（加 cmd 入口） （理由：____） |
| P-3 | SanitizedMessageRef 无 occurrence | 完整 identity/occurrence 映射仅压缩层成立，sanitized 层只有 identity | ☐ 扩展 occurrence（改动 V2 metadata 结构，需迁移） ☐ 接受现状（压缩层已覆盖完整映射场景） （理由：____） |

## 三、附带架构专项（立项裁决）

| # | 专项 | 现状 | owner 裁决 |
|---|---|---|---|
| A-1 | S7-4 节点状态单模块（D09） | 写点 ~38 文件/4 层，反馈面/ProbeSync/Confirm/write-through 已收敛但未单模块化 | ☐ 立项专项设计 ☐ 维持现状（D09 目标降级） （理由：____） |
| A-2 | S8-F6 UA 关键词/客户端类型枚举 SSOT 化 | 真重复 2 份 + 封闭集枚举 3 份（R61 §六.4） | ☐ 立项 ☐ 维持现状（理由：____） |

## 执行纪律（owner 确认后的清理轮）

1. 清理轮独立于审计轮执行；每删一项跑 `go build ./...` + 相关包全测。
2. C5 需同时处理 `tests/local/gateway/main.go`（harness 退役或改造）。
3. 全部删除以 git 历史为恢复手段，不做软删除/废弃注释残留。
4. 清理完成后在本清单表格补「执行 commit」列并归档至 docs/audit/playbook/。
