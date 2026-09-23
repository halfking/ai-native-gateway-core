# R58 48h 审计轮 Handoff（2026-09-23）

> 承接 R57（49ca63322 → 本轮）。轮文档：
> docs/audit/2026-09-23-r58-48h-audit-round.md（§三 真机注入含根修全链路）。

## 1. 任务概要（Mission Summary）

R57 §六 按序三项：①chunk_buffer 三验证裁决 ②error_detector_ring 换型
评估 ③§11.6 真机 154/245 故障注入。①②按证据裁决"不接线、删除"（前提
证伪）；③在 245 实锤 §11.6 post-DONE 第二终端泄漏（P2）并当轮根修 +
真红钉桩——本轮主交付。

## 2. 任务进度（Progress）

- ✅ **chunk_buffer 不接线收口**：三验证全败——s8_burst 场景实为
  `stream:false`（基准锚点错位）+ 最新实跑 871 req/s 否证 "~600 封顶" +
  idle-flush 无定时器（TTFT 上界无界，非宣称 50ms）+ 接线必拆 40 处语义
  Flush 配对且与 ConnectionMonitor/AttemptCommitGate 互斥。删除两文件。
- ✅ **error_detector_ring 零调用收口**：换型点 WeightedRouter.ErrorDetector
  生产零注入（WeightedRouter 生产零持有，仅 tests/local 用）；内存主张失真
  ~25×（实测 legacy 624B vs ring 156B，比 ~4× 非宣称 ~100×）。删除两文件
  + 测试；credentialhealth 生产面不受影响。
- ✅ **§11.6 真机注入（245）**：admin API + 环回 mock（按 model 名切
  broken/stall 模式）全链路打通。主契约面全部合格（200 保持/chunk 可见/
  envelope/恰 1 个 [DONE]），但 **[DONE] 后多一帧 model_not_found**——
  survival Terminal seam 渲染终态后返回 FinalError，chat 共享错误面
  prewarm 分支无"线上已终态"感知再写（绕过 R56 P2-1 闩锁）。
- ✅ **根修 + 钉桩**：survival_wiring 终态哨兵 `errSurvivalTerminalRendered`
  （多 %w 保 As-链）+ handler 错误面黑洞 writer（bookkeeping 保留、线上写
  静默）；`survival_committed_broken_e2e_test.go` 真红验证（stash 修复逐
  字节复现 245 泄漏）。messages/responses 既有 usedSurvival 早退不受影响。
- ✅ **245 清理对账**：mock 杀/绑定删/凭据软删/provider 软删/key 吊销/
  canonical 模型禁用/tmp 清除；request_logs 审计行保留。
- ⏸ **154 注入**：与 245 同库（172.16.2.210/llm_gateway）+同二进制
  （9cbd60e4），预修复重做零新信号，登记部署后复验（harness 可重放）。

## 3. 当前状态（Current State）

- 分支 main；fetch 时点 origin 0/0 无分叉；零新增迁移（下一可用 **741**
  维持，740 已占用）。
- 245 canary@8782 active build_seq 2209；154 canary@8781 active 2211——
  均不含本轮修复（post-DONE 泄漏两侧皆在）；**下次部署双侧携带**。
- 工作树有并行会话 deploy-local.sh mavis-trash stdout 修复（未提交 WIP）
  及 VERSION/version.json 漂移——精确路径提交，不代提交。
- 注入 harness 脚本未入库（一次性.tmp 已清）；重放步骤完整记载于轮文档
  §3.1/§3.6（admin API 端点序 + mock 模式定义 + 清理对账清单）。

## 4. 下一步（Next Steps）

1. **部署后复验**（245+154 例行部署携带 R58 修复）：(a) survival
   post-DONE 静默（重放 §3.1 harness）；(b) 737→740 迁移序与探测健康
   视图族回归；(c) client_ips 真源表现（R57 §六.3 顺延项合并核对）。
2. **prompt_compress 生产面对齐裁决**（批次 3 残留）：
   internal/ir/prompt_compress.go 零消费，transformation/ctx_compress.go
   为真生产面——收口或对齐。
3. **R56 §三.5-8 顺延**：promote 饥饿 gauge、lite 幽灵轮 view 过滤、
   UA 三副本 SSOT、bg 裸 go 盘点；B6/B11（741 起）/B13；缺省 max_tokens
   直发归属；tool_choice 裸字符串（待 8782 实测）。

## 5. 关键事实（Key Facts）

- **§11.6 终态唯一性教义**：survival 终态在 wire 上的不变量 = 恰一个
  envelope + 恰一个 [DONE] + 其后零帧。执行器闩锁（MarkTerminalRendered）、
  survival Terminal seam、handler 错误面三层都要服从；跨层感知靠
  `errSurvivalTerminalRendered` 哨兵（survival_wiring.go）。
- **测试写 survival e2e 的坑**：模型名撞 `isUnstableModel` 表（glm-5.2/
  minimax-m3/minimax-text-01 = 10s/50 扩展 holdback）会让首 attempt 字节
  全被 holdback 丢弃走 fail_closed 而非 committed_output——用中性模型名。
- **admin 注入定式**：/api/auth/token JWT（legacy sk- key 打 admin 401）；
  建凭据勿带 concurrency_limit（撞 fp_slot 联动 check 约束）；绑定
  raw_model_name 可与 outbound_model_name 解耦（mock 按出站名切模式）。
- **真机注入前置勘察**：154/245 同库同二进制时，单机注入结论对另一机
  不构成独立证据；DB host 与 git_sha 先核再动。
- chunk_buffer/ring 的教训已入 D17 报告：移植组件的头注释性能主张必须
  落库前实证（s8_burst 场景 stream:false 这种锚点错位，grep 三分钟可证伪）。
- WeightedRouter 生产零持有（仅 tests/local/gateway 用）——其内部实现
  优化（如 R57 minheap）零生产价值，后续审计不再重复评估其内部字段。

## 6. 阻塞 / 风险（Blockers / Risks）

- **post-DONE 泄漏在 245/154 生产面存活**：严格 SSE 解析器收到 [DONE] 后
  的多余 error 帧可能报错/计入失败统计；非严格 SDK 忽略。修复未部署前
  该风险持续存在（触发条件：单候选 + committed-then-EOF/failover 耗尽）。
- 注入期间 245 出现过 wait_recovery_window 重试风暴面（keepalive 保持），
  均为测试模型自身通道池，未波及生产模型路由。
- credential_model_index_hot 对已删绑定的残留行随 5 分钟桶轮转自愈。

## 7. 下一轮提示词（R59）

见轮文档 §七。

## 8. 引用（References）

- docs/audit/2026-09-23-r58-48h-audit-round.md（§三 注入与根修全链路）
- domains/streaming/survival_wiring.go（哨兵）+ handler.go（黑洞）+
  survival_committed_broken_e2e_test.go（真红钉桩）
- tests/stress/results/report.json（s8_burst 最新实跑，chunk_buffer 否证）
- tests/48h-audit/D17-code-hygiene/reports/latest.md（R58 追加段）
