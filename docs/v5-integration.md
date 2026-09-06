# LLM Gateway · v5 整合对齐文档

> 本文档取代 `v4-融合对齐.md` 成为最高层对齐依据；v4-融合对齐.md 冻结归档（原位保留）。
> 总方案：`../../docs/2026-09-06-multi-agent-platform-integration/00-总纲-多智能体工作平台整合方案v5.md`（工作区 docs/ 下）

## 本项目在 v5 中的定位
- LLM 统一网关（canonical：本目录）；一切端到端 LLM 调用必经
- 会话压缩唯一权威（SessionCompressor, LOSSLESS_FIRST）
- 模块设计：工作区 `docs/2026-09-06-multi-agent-platform-integration/modules/llm-gateway/README.md`

## 资源对齐（实测 2026-09-06）
- PG：共享 `llm-gateway-pg`（gateway 库，migrations/ 权威）
- Redis：共享 `nbjl-redis` DB5，前缀 `llmgw:`
- 运行：`llm-gateway-local-8782`，宿主 127.0.0.1:8782

## 本仓库待办（对应 v5 Phase）
- [ ] Phase 0-D4：deploy-local.sh 从 heatmap 单特性脚本泛化为标准入口（迁移检查→build→up→health→--down/--dry-run），文件名不变
- [ ] Phase 0-D6：与 llm-gateway-go-3/-5、顶层副本的差异盘点（删除需用户确认）
- [ ] Phase 2：X-Correlation-ID 透传 + 审计可按其检索；/metrics 按 tenant/project/model/task_type 出面板

## 铁律
- 部署只走 `./deploy-local.sh`；共享资源永不创建/停止/删除；同 kind 服务不得多实例。
