# R35 子代理报告原文（2026-09-17，窗口 b9d9a8ba5..2f3151a27 增量 + R33 遗留）

8 路只读域子代理（D01/D02/D06/D08/D11/D14/D16/D17），派发提示词 = 各域文档 §5 + 主代理注入窗口与专项线索。
主代理逐条亲读复核结论见 docs/audit/2026-09-17-r34-48h-audit-round.md §一/§复核裁决。

- agent-D01.md —— 影子轮落库链健康；audit 影子轮 model="auto" 后门（并入 R35-P1a（撤并采用并行 R34 上游实现） 处置）；handoff 误标线索（复核后降级：生产不在响应链）
- agent-D02.md —— advisory 帧契约逐字段与文档一致；混合模式文档缺口（R35-P3e）；注释断言过宽（R35-P2b 顺修）；mode=handoff 测试缺口（已补）
- agent-D06.md —— F1→G2 不变式破坏线索（复核确认为 R35-P1b）；goal 影子轮 claim 竞态（登记）；lite 模式健康
- agent-D08.md —— credential_state_log 决策评估（推荐 A 带双门 or 按 ops 快照结案；R34 采结案+登记）；恢复链三道覆盖核实
- agent-D11.md —— F1 半修升级：emitTuningSignal 全路径死 + 业务 auto 成功轮镜像剔除（R35-P1a（撤并采用并行 R34 上游实现） 根因链）；auto 灰度口径混入合成流量（登记）
- agent-D14.md —— X-Gw-* 头客户端可注入（R34-P2 登记+方案）；keyring 无锁缝隙实为潜在非活竞态（R35-P3g 注释钉契约）；advisory 无持久注入面
- agent-D16.md —— Path 1.5 抢占模型轮换（R35-P2b）；audit_auto_fix 误标（R35-P2a）；goalrun scheduler 死脚手架（登记）；告警覆盖缺口（R35-P3b）
- agent-D17.md —— 签名一致性/env 三方一致/gofmt 窗口干净；双重截断（R35-P3c）；误导注释×2（已修）；41 文件存量 gofmt 债（登记）
