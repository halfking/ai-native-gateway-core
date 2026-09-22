# R56 · D11 auto 模型 · 48h 审计结论

> 时间：2026-09-23 · 与 D04 同批审计

## 结论
auto 全链路在位：入口 maybeResolveAuto（model=="auto"，无 decider 落默认模型）→ decider 组装（启发式+tuning 分类器、LLM 兜底默认 Disabled、DB ProfileStore、Redis intent cache）→ 非聊天路径 auto_route_nonchat → dispatch "auto"/空归入 auto 队列桶 → 模型切换门 dispatch_v2.allow_model_change 默认 false。灰度：RT-1 IQ 门/RT-3 热门加权默认 off 且 off 路径字节级钉桩——与 Wave2 灰度报告"维持 off、租户级+V3 影子通道"裁决一致。本轮改动（F15 债务/B14 看门狗）对 auto 解析结果一视同仁，无交互风险。
