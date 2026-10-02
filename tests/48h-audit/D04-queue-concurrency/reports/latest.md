# R56 · D04 队列并发 + D11 auto · 48h 审计结论

> 时间：2026-09-23 · 窗口 2de612429..80c74af01 · 执行：主代理+D04 子代理亲读复核

## 发现与处置
| 级别 | 项 | 处置 |
|---|---|---|
| P2 | B14 看门狗走保温 EXPIRE 脚本——"强制释放"不释放（槽仍计占用+idle gate 重置+TTL 反而刷新）；breach 计数 TOCTOU；AfterFunc timer 不可停 | ✅ ReleaseHard（DEL 硬释放）+计数移入 performed 后+watchdogTimer Stop；miniredis 钉桩 ×2 |
| P3 | B14 300s 硬顶不随 fp_slot_ttl 热更（<300s 时 key 已自然过期，指标口径失真） | ✅ 随 ReleaseHard 自然解决（not-owned → 不计 breach） |
| P3 | minheap_topk 孤儿模块（零生产调用方，O(N²) SelectTopN 仍在产）——Handoff-B 已知挂账非静默遗漏；Push NaN 占位/SortedDesc O(K²) 接线前修 | 登记（Handoff-B 第一批） |
| P3 | rpm 债务+refill 封顶交互属设计预期；建议对 dispatch_extra_upstream_calls_total 加告警阈值 | 登记 |

## 核实为健康
F15 债务补记无重/漏计（4 个额外调用点各一次+tpm 同成本模型+测试钉桩）；队列生命周期 complete CAS 单次终态+ctx 放弃兜底+overflow 终态+drainer recover；base_worker 监督循环 recover+退避封顶+Stop 不悬挂+done CAS 恰关一次；阈值集中化零漂移。D11 auto 全链路在位（maybeResolveAuto→decider→auto 桶→切换门默认 false；灰度 RT-1/RT-3 维持 off 与报告一致）。

## 遗留
Handoff-B 接线三批（R55 §九 #4-6）。
