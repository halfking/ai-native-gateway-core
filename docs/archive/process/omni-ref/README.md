# OmniRoute 集成文档（llm-gateway-go）

本目录是对 `docs/omniroute-ref/` 的**审计与翻译补充**。当前 checkout 中原始 `omniroute-ref` 方案正文无法从可用 refs 恢复；`docs/omniroute-ref/` 现存的是带证据等级的重建草案，不应被当作历史原文。这里聚焦“参考源码 / Go 真实代码 / 新设计边界”的核对，以及 TypeScript → Go 的翻译方法论。

## 文档结构

```
docs/
├── omniroute-ref/                      # 可审计的重建草案（不是恢复的历史原文）
│   ├── 00-IMPLEMENTATION-ROADMAP.md    # 基线、证据等级、阶段门禁
│   ├── phase1/{01-provider-expansion,  # P1 provider / R1 routing / C1 Lite
│   │           02-advanced-routing,
│   │           03-lite-compression}.md
│   ├── phase2/{04-deep-compression,    # C2-C4 deep compression / M1 MCP
│   │           05-mcp-server}.md
│   └── phase3/{06-a2a-protocol,        # A1 A2A / R3 Fusion
│               07-fusion-routing}.md
└── omni-ref/                           # 本目录：审计 + 翻译融合指南
    ├── README.md                       # 本文件
    ├── 00-AUDIT-EXISTING-DOCS.md       # 真实代码与参考源码核对
    └── 01-TS-TO-GO-FUSION-GUIDE.md     # TS→Go 翻译方法论
```

## 阅读顺序

1. **`omniroute-ref/00-IMPLEMENTATION-ROADMAP.md`** —— 了解基线、证据等级、阶段门禁和非目标。
2. **`omni-ref/00-AUDIT-EXISTING-DOCS.md`** —— 确认哪些内容是 Go 当前事实、哪些来自参考 checkout、哪些仍待证据。
3. **`omni-ref/01-TS-TO-GO-FUSION-GUIDE.md`** —— 阅读类型、错误、并发、SQLite→PG 与装配边界。
4. **按特性读 `omniroute-ref/phaseN/0X-*.md`**，对照对应的参考源码和 Go 目标边界。

## 关键结论

- Go 当前的 Candidate/Router/Executor/Compressor/metatools 路径已核实；这不等于原始 OmniRoute 方案正文已恢复。
- provider catalog、MCP、A2A、Fusion 和新的压缩 stage 仍需按重建草案中的 `NEW-DESIGN`/`MISSING-EVIDENCE` 门禁实施。
- 翻译原则：规则数据与纯算法可对照迁移，框架胶水用 Go 原生重写。
- 融合方式：装配式注入（`main.go`）+ 保持 executor/router 依赖边界 + feature flag 默认关闭。

## 源参考与快照

- OmniRoute 参考 checkout：`/Users/xutaohuang/workspace/ai/OmniRoute`
- package metadata：`3.8.49`
- 已核对源码 snapshot：`c8f1d62de5d223aa209c7b0f7e09bb35382a4d2e`（不是 package release commit 的证明）
- Go 当前规划基线：`052dbd02034d1e04a92c4e216b87e5b66e12dcee`
- 关键参考实现：`open-sse/services/compression/`、`open-sse/mcp-server/`、`src/lib/a2a/`、`src/lib/memory/`、`src/lib/skills/`
