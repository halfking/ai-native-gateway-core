# OmniRoute 集成文档（llm-gateway-go）

本目录是对 `docs/omniroute-ref/`（特性设计方案）的**补充**：聚焦"方案 vs 真实代码的审计"与"TypeScript → Go 的翻译与融合方法论"。特性设计本身见 `docs/omniroute-ref/`。

## 文档结构

```
docs/
├── omniroute-ref/                      # 特性设计方案（已有，7 份 + 路线图）
│   ├── 00-IMPLEMENTATION-ROADMAP.md    # 总路线图 / 工期 / KPI / 资源
│   ├── phase1/{01-provider-expansion,  # P1 提供商 / R1 路由 / C1 Lite
│   │           02-advanced-routing,
│   │           03-lite-compression}.md
│   ├── phase2/{04-deep-compression,    # C2-C4 深度压缩 / M1 MCP
│   │           05-mcp-server}.md
│   └── phase3/{06-a2a-protocol,        # A1 A2A / R3 Fusion
│               07-fusion-routing}.md
└── omni-ref/                           # 本目录：审计 + 翻译融合指南
    ├── README.md                       # 本文件
    ├── 00-AUDIT-EXISTING-DOCS.md       # 7 份方案 vs 真实代码逐条核对 + 3 处修正
    └── 01-TS-TO-GO-FUSION-GUIDE.md     # TS→Go 翻译方法论 + 参考代码清单 + 融合技能
```

## 阅读顺序

1. **`omniroute-ref/00-IMPLEMENTATION-ROADMAP.md`** —— 了解全局（3 阶段 / 工期 / KPI）
2. **`omni-ref/00-AUDIT-EXISTING-DOCS.md`** —— 确认方案可执行性 + 3 处修正（provider 非常量、CompressMessagesIfNeeded 同名两物、ToolRegistry 在 registry 包）
3. **`omni-ref/01-TS-TO-GO-FUSION-GUIDE.md` §1** —— 通用翻译方法论（类型/错误/并发/SQLite→PG）
4. **按特性读 `omniroute-ref/phaseN/0X-*.md`**，对照 `01-...GUIDE.md` §2 该特性的"可参考代码清单"

## 关键结论

- 方案事实基础**扎实**（Candidate/Router/Executor/Compressor/metatools 路径均验证一致）。
- 翻译原则：**规则数据与纯算法直接搬，框架胶水 Go 原生重写**。
- 融合方式：**装配式注入**（main.go 统一装配）+ **不污染 executor/router** + **feature flag 默认 off**。
- 下一起始迁移号：**463**。

## 源参考

- OmniRoute 仓库：`/Users/xutaohuang/workspace/ai/OmniRoute`（v3.8.49）
- 关键参考实现：`open-sse/services/compression/`、`open-sse/mcp-server/`、`src/lib/a2a/`、`src/lib/memory/`、`src/lib/skills/`
