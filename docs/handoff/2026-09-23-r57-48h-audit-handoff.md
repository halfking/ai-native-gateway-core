# R57 48h 审计轮 Handoff（2026-09-23）

> 承接 R56（9e99850f8 → 本轮 9f785e1ff，6 提交 + 复审修正）。轮文档：
> docs/audit/2026-09-23-r57-48h-audit-round.md（含 §七 批判式复审）。

## 1. 任务概要（Mission Summary）

R56 §三 遗留 1-4 项收口：B7 virtual_ip 假名数据源级修复（迁移 740）+
ProbeSync/ProbeConfirm 双闸 + D4 推断对称化 + Handoff-B 首批（minheap_topk
接线）。批判式复审（§七）已当轮完成：全部"声称完成"重新实证，三处问题修正
（live 测试 template0 健壮性、740 头注释过时描述、INDEX 误重定向）。

## 2. 任务进度（Progress）

- ✅ **B7 + 740**：view 链（底/中层 regexp + 顶层 734 骨架全量重建）投影真源
  client_ip；列契约 113/109/110 → 115/110/111；dev 库 down/up 双向实跑收敛
  + 台账行在位。消费面三层接线：rollupDims client_ip 维度（virtual_ip 保留
  遗留对照；死代码 rollupVirtualIP 删除）、看板 API 键 virtual_ips→client_ips
  （web + 8 语言 i18n）、内存累积路径 entry.ClientIP。pseudo 标注按构造消解。
- ✅ **738 六点缺口**：Go 自愈组合体（canonicalV2DDL 等）补
  credits_rate_multiplier/client_ip，113/115 宽度门控；离线 + live 契约测试
  双绿（ensure↔710+734+738+740 逐字节等价、down 全链、幂等、数据语义）。
- ✅ **ProbeSync/ProbeConfirm**：获取序 per-cred→全局；ProbeConfirm ping 前
  有界等闸 1.5s，饥饿 fail-open + slot_starved 计数器；钉桩 ×2 + -race 绿。
- ✅ **D4**：流式 total_tokens 推断对齐非流式双向 + 守卫；变体表 +2 用例。
- ✅ **minheap_topk 接线**：SelectTopN O(N²)→O(N log K)，平序语义保持。
- ⏸ **chunk_buffer / error_detector_ring / prompt_compress**：评估后缓批
  （SSE 语义 Flush 契约 / 路由 API 换型；需专门小轮 + TTFT/终端帧/bench
  验证），设计要点在轮文档 §四。

## 3. 当前状态（Current State）

- 分支 main；本轮推送 9f785e1ff（c165e0bce..9f785e1ff，6 提交；变基吸收并行
  会话 4 提交，db-changelog 双块保留 + 245 部署状态勘误进轮文档 §六.3）。
- 迁移台账：**740 已占用**；下一可用 **741**（B11 从 741 起）。
- 部署位面：245 已应用 737-739（seq 2193）；**740 三台均未应用**，本机 dev
  已带外收敛（台账行 + 幂等）。154 未应用 737-740。
- 本机环境：PG 容器与 8782 网关由并行会话于 09-23 ~11 时重启/部署，boot 自愈
  已重建探测健康视图族（v_model_health_dashboard/v_probe_system_health 实查
  在位）；live 契约测试须 template0 建库（环境 locale 漂移，已修入测试）。

## 4. 下一步（Next Steps）

1. **Handoff-B 批次 2**：chunk_buffer 接线——先裁决接入形态（responseSink
   包装 vs 逐写函数内嵌），硬约束 = 终端帧及时性（§11.6/R27 闩锁语义）+
   TTFT 不回归 + s8_burst 压测主张验证。
2. **批次 3**：error_detector_ring 换型（weighted_router.go:55 字段类型 +
   RingBackedDetector 零调用收口，内存/分配主张 bench）；prompt_compress 与
   transformation/ctx_compress.go 生产面对齐裁决。
3. **R56 §三.5-8 顺延**：§11.6 真机 154/245 故障注入；promote 饥饿 gauge、
   lite 幽灵轮 view 过滤、UA 三副本 SSOT、bg 裸 go 盘点；B6/B11（741）/B13；
   缺省 max_tokens 直发归属；tool_choice 裸字符串（待 8782 实测）。
4. **740 部署跟踪**：下次例行部署核对（a）升级通道 737→738→739→740 顺序；
   （b）首启后探测健康视图族回归；（c）client_ips 饼图真源表现（dev 面以
   后台合成流量为主，生产观感待 154 真实流量）。

## 5. 关键事实（Key Facts）

- view 链扩列定式：**禁用 v.\* 星**（展开冻结于 CREATE 时刻）——迁移、Go
  自愈、down 三方字节等价必须显式中层列清单（与 middleWrapperCols 同查询），
  新列以 `v.<col>` 文本引用固定内层末尾（738 插入位）；live 等价测试重放前
  必须复位包装视图为原始冻结形态（否则 738 文本插入撞 42702）。
- DROP CASCADE 会带走探测健康视图族 → db.ensureProbeHealthDashboardViews
  每次启动 DROP+重建自愈；部署后核对两视图回归。
- ProbeConfirm 饥饿 fail-open 教义：容量饥饿非节点状态证据，不降级；
  ctx 取消维持 fail-closed。
- INDEX 聚合器是 stdout 重定向式：`bash scripts/aggregate-reports.sh >
  reports/INDEX.md`（重定向到 /dev/null 等于没跑）。
- 并行会话持续活跃（stress 容量轮 + 部署）；提交前 fetch + `git diff
  HEAD...origin/main` 核对重叠，迁移编号以远端为准（下一个 741，用前再核）。

## 6. 阻塞 / 风险（Blockers / Risks）

- 740 未上三台：rollup/看板 client_ip 维度在无 740 的库上每分钟报
  column does not exist（回退到旧二进制才会发生；迁移先行于服务的部署序
  已保证——但灰度/回滚窗口需知悉）。
- 历史 client_ip 空窗（轮文档 §七）：部署前窗口 IP 饼图为空，无回填计划。
- baseline SSOT 落后迁移链（credits_rate_multiplier/client_ip 不在 baseline
  视图表），新装靠启动序列收敛——既有模式，未新增活体新装验证。
- 本机 PG locale 漂移根因未追（容器重启后裸 CREATE DATABASE 22023）——
  测试已免疫，实例本身健康（数据/迁移/视图全在位）；若复现扩大化需查
  并行会话的 PG 容器管理流。

## 7. 下一轮提示词（R58）

```text
承接 R57，轮文档 docs/audit/2026-09-23-r57-48h-audit-round.md §六 + handoff
docs/handoff/2026-09-23-r57-48h-audit-handoff.md §4 按序执行：
首项 = Handoff-B 批次 2 chunk_buffer 接线（终端帧及时性 + TTFT + s8_burst
三验证先行裁决接入形态），次项 = error_detector_ring 换型评估（零调用
收口或 bench 后接线），再次 = R56 §三.5 §11.6 真机 154/245 故障注入。
知识库入口：tests/48h-audit/README.md + docs/audit/playbook/conventions.md；
窗口 = git log --since="48 hours ago"。
硬约束：子代理线索非事实；handoff『已根修』断言必须多 bash 版本 subshell
实跑；新增迁移前 fetch 核对远端编号（下一可用 741，740 已占用）；视图链
任何改动必须同步 db/request_logs_view_schema.go 自愈体 + 契约测试（禁 v.*
星）；例行部署走 deploy-local 全流程并核对 active-version 字面量。
```

## 8. 引用（References）

- docs/audit/2026-09-23-r57-48h-audit-round.md（含 §七 批判式复审）
- docs/audit/2026-09-23-r56-48h-audit-round.md §三（本轮入口）
- sql/migrations/startup/740_view_chain_client_ip.sql（+ .down）
- db/request_logs_view_schema.go + db/view_schema_v2_contract_test.go
- scripts/apply-db-revision-sequence.sh（740 登记）
- docs/db-changelog.md（R57 段）
