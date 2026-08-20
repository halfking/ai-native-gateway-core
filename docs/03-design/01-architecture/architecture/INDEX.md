# architecture · 索引

> **事实快照：** 2026-08-21  
> 本目录的权威阅读入口是 [`README.md`](README.md)。自动生成索引不应替代当前架构事实。

## 核心文档

- [ARCHITECTURE.md](ARCHITECTURE.md) — 当前系统架构、生产路径、数据与控制面。
- [runtime-request-flow.md](runtime-request-flow.md) — 请求、attempt、turn、charge、retry 与持久化时序。
- [routing-and-state.md](routing-and-state.md) — URSM、资源治理、策略和路由状态。
- [optimization-roadmap.md](optimization-roadmap.md) — 需要解决的问题、代码指导、测试和波次。
- [omniroute-integration-boundary.md](omniroute-integration-boundary.md) — OmniRoute 集成边界。
- [REPO_LAYOUT.md](REPO_LAYOUT.md) — 仓库布局与入位规则。

## 维护说明

新建架构专题后，请更新 `README.md` 和本索引。不要写入 `docs/docs/...` 之类的重复路径；链接必须相对当前文件可解析。
