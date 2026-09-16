# D06 — 双重存储架构（全量 vs 本地简化）

> 领域编号: D06 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：全量模式（pg+redis+memory+files）与本地简化模式（sqlite+memory+files）的开关、行为对齐、能力降级边界；lite 模式下被禁用组件的收口。
**不管**：pg 内部分区结构（D07）；缓存 provenance（D03）；统计口径（D10）。

## 2. 参考基线

设计文档：
- `docs/design/lite-mode-storage-design.md` — 双模式存储架构设计（核心）
- `docs/design/lite-mode-index.md`（系列索引）、`lite-mode-architecture-decisions.md`、`lite-mode-deployment-comparison.md`
- `docs/storage/README.md`（双模式总览）、`deployment-guide.md`、`troubleshooting.md`

代码入口：
- `storage/`、`internal/fsstore/`、`internal/redis/`、`internal/dbx/`
- 模式开关：配置装载路径（`config/`、`configs/`）中 lite 相关 env

## 3. 检查清单

1. **开关收口**：full/lite 由统一开关决定；lite 六 env 收口禁 redis（清单以 lite-mode 系列文档为准），窗口内新增功能若依赖 redis，必须在 lite 下有降级路径或显式禁用。
2. **行为对齐**：同一业务操作在两模式下语义一致（成功/失败/幂等）；新增存储调用点若用了 pg 专属能力（partition/RLS/advisory lock/UPSERT 冲突子句），sqlite 侧有等价实现或编译/启动期阻断。
3. **文件存储布局**：lite 目录布局符合 docs/storage/README；文件缓存与附件存储路径不因模式切换而错位。
4. **分区管理器**：lite 下不启动（既有不变量），窗口内不引入 lite 下会误启动的调用。
5. **能力边界诚实**：lite 不支持的功能在 API 层显式返回"不支持"，而非静默空实现。
6. **部署文档同步**：两模式的部署/排障文档与代码开关现状一致（文档说禁的 env 代码真的禁）。

## 4. 历史回归点（轮末回注区）
- [R35 09-17] GLOBAL_G2 不变式与 claim/镜像双门一致性：镜像排除门（isInternalAutoEntry）与 is_final_success claim 谓词必须共享同一判定（现 telemetry.IsInternalAutoEntry 单一事实源）；任何让成功终态携带 IsAutoRequest 的改动都会同时翻转两门输入——只改一侧必破 G2（gt_/gs_ 内部回环行 claim 但不镜像=G2 恒>0，R35-P1b）。post-F1 二进制部署后首个每日观察是验证点

- [R30] full/lite 开关明确、lite 六 env 收口禁 redis、lite 缓存 L1+L1.5 文件级、分区管理器 lite 不启动 —— 健康面基准

## 5. 子代理派发提示词

```text
你是 D06（双重存储架构）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D06-dual-storage-mode.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内新增的存储调用点是否在 lite 模式下有对等实现或显式禁用。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```
