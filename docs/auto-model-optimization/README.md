# AUTO 模型优化：文档导航

本目录只描述 AUTO 路由优化的已批准设计，不代表生产 Go/SQL 已全部实现。

## 文档清单

| 文件 | 内容 |
|---|---|
| [01-database-schema.sql](./01-database-schema.sql) | 现有 `auto_route_selections` 事实表、8 小时热数据、月分区与统一 all view 设计 |
| [02-architecture-design.md](./02-architecture-design.md) | 端到端架构、数据边界与路由反馈闭环 |
| [05-storage-optimization.md](./05-storage-optimization.md) | 存储最小化、分区、保留与查询策略 |
| [06-free-llm-integration.md](./06-free-llm-integration.md) | 动态免费池 shadow、低价直连后备与配额治理 |
| [07-human-annotation-workflow.md](./07-human-annotation-workflow.md) | 只使用元数据的人工标注流程 |
| [08-ml-training-pipeline.md](./08-ml-training-pipeline.md) | 结构化特征模型的训练、评估与发布 |
| [09-onnx-integration-go.md](./09-onnx-integration-go.md) | 结构化特征模型的 Go/ONNX 推理边界 |
| [10-free-llm-integration.md](./10-free-llm-integration.md) | 免费/低价供应商调研口径与运行策略（与 06 保持一致） |
| [11-implementation-roadmap.md](./11-implementation-roadmap.md) | 分阶段实施与验收标准 |

## 核心约束

1. **唯一事实表**：路由选择与结算指标以现有 `auto_route_selections` 为唯一事实来源；不新建平行的 request/classification/metrics 事实表。
2. **只存元数据**：不存储 prompt、messages、response、summary、可逆内容片段、关键词原文或会话正文。允许保存不可逆的哈希/指纹及结构化特征。
3. **数据布局**：热数据窗口为最近 8 小时；历史数据按月分区；通过统一 all view 提供跨热区与月分区查询语义。
4. **人工标注**：标注任务只复制请求/路由元数据与结构化特征，不复制正文；正文仍留在原有受控系统边界内。
5. **ML 初期**：先训练结构化特征模型（规则/线性模型/树模型等），不以文本编码器或原始内容训练作为首期前提。
6. **免费 LLM**：任何免费额度都必须运行时读取供应商官方信息并视为动态基准；免费池只做 shadow/受控实验，生产必须有低价直连后备和本地规则/模型降级。

## 阅读顺序

1. [架构设计](./02-architecture-design.md)
2. [数据库与存储](./01-database-schema.sql) → [存储优化](./05-storage-optimization.md)
3. [人工标注](./07-human-annotation-workflow.md) → [ML 管道](./08-ml-training-pipeline.md) → [Go/ONNX](./09-onnx-integration-go.md)
4. [免费池策略](./06-free-llm-integration.md) → [实施路线图](./11-implementation-roadmap.md)

## 口径说明

文档中的准确率、延迟、成本和配额均为待实测或动态配置的目标/基准，不能表述为已达成结果、固定价格或永久免费承诺。
