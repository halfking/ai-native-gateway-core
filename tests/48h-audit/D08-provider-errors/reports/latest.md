# R56 · D08 供应商错误处理 · 48h 审计结论

> 时间：2026-09-23 · 窗口 2de612429..80c74af01 · 执行：主代理+D08 子代理亲读复核

## 发现与处置
| 级别 | 项 | 处置 |
|---|---|---|
| P2 | ProbeSync 双层信号量顺序（全局槽先取+per-cred 后取）——同凭据 ≥6 模型突发可占满 8 全局槽，其它凭据热路径同步探测被拖数十秒（非死锁） | 登记 R57 首批（获取顺序对调或等待时归还全局槽） |
| P2 | ProbeConfirm 绕过 per-cred ≤2 闸（去重键是 (cred,model)）——N 模型真实故障派生 2N 直连 | 登记 R57 首批（confirm 走 credSemaphore 或按 credential 去重） |
| P2 | B5 四键默认新模式下 2/4 死值：activeProbe.Start 被跳过但 submitter 仍接线→向无消费者队列投递（running 泄漏+WARN 刷屏） | ✅ submitter 接线加 ！useNewProbeMode() 门（threshold/timeout/workers 三键消费在位） |
| P3 | per-cred ≤2 闸未覆盖 probe_queue_worker 执行面（Workers=5 可并发同凭据多模型；后台有 batch 兜底） | 登记设计口径 |

## 核实为健康
A4 429 剔除断路（早退+HALF_OPEN ReleaseProbe+两策略表删项+3min 降权保留+测试钉桩）；B2 老化恢复路径存在且不触碰 404 三级冷却；selfcheck 3 天范围 fail-open；B3 真 nil+autoseed 防抖并发安全；错误记录闭环（candidate_failure_logs→supplier_errors_hot→admin 凭据详情两接口）+ errorsx 分类 SSOT 全消费；401/403/解密失败路径与本轮改动零冲突。

## 遗留
见轮文档 §三.2。

## R57 追加（2026-09-23）
| 级别 | 项 | 处置 |
|---|---|---|
| P2 | ProbeSync 双层信号量顺序 | ✅ 获取序对调 per-cred → 全局（goroutine 内）：突发不占全局槽，单凭据经 ≤2 闸最多支配 2 槽；锁序不变 |
| P2 | ProbeConfirm 绕过 per-cred ≤2 闸 | ✅ ping 前有界等闸（1.5s，睡眠窗口不占槽）；饥饿 fail-open（不降级）+ slot_starved 计数器；ctx 取消维持 fail-closed；钉桩 ×2 + -race 绿 |
| P3 | per-cred ≤2 闸未覆盖 probe_queue_worker 执行面 | 维持登记（后台 batch 兜底，设计口径） |
