# R56 · D01 IR 生命周期 · 48h 审计结论

> 时间：2026-09-23 · 窗口 2de612429..80c74af01 · 执行：主代理+D01 子代理亲读复核

## 发现与处置
| 级别 | 项 | 处置 |
|---|---|---|
| P1 | collapseRepeatedText `out[:i]` 二段命中暴露零值 Message+丢尾部（prompt_compress.go 改前 271-321） | ✅ 本轮重写为区间拼接+尾部 append；双 run+gap+tail 回归测试入库 |
| P2 | GAP-3 残留：单块空 text 走 `content:""` 字符串路径仍直发（serialize_anthropic.go:422-427） | 登记挂账（与 auto/none/any 同族，待 8782 实测统一裁决） |
| P2 | tool_choice auto/none/any 裸字符串 vs Anthropic 对象形态（:848-850，Wave5 顺延项实锤） | 登记挂账（维持 Wave5 裁决：需上游兼容性实测证据） |
| P2 | max_tokens=0 直发（:14-17；golden 钉住 0 vs 手写 4096）——真实暴露面=OpenAI 入站→Anthropic 系上游 400 误判节点故障 | 登记挂账（责任归属裁决） |
| P2 | parse_ollama_stream 终端行 Type 覆写 Delta+累积文本塞 Delta.Content 契约自相矛盾（:83,:97-102；当前零生产 caller） | 登记挂账（接线前修） |
| P3 | GAP-1 邻接：仅提取首条 system，后续 system 透传 role:"system" 会 400（parse_openai.go:652-675，既有行为） | 登记 |

## 核实为健康
GAP-1/2/3 修复在位且 golden 钉住；GLM 错误通道委托+拼写容错 fail-closed；paramreg 升级契约钉桩；Metadata 覆盖多 tag/项目/总轮次；附件媒体（Image/Media/Document/Audio/PDF）齐备。缺失面（调度瀑布/流程跟踪/凭据引用/已压缩标记）确认为 transport 层承载的设计分层，非缺口。

## 遗留
见轮文档 §三.7。
