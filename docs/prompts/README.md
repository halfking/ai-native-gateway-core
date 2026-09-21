# 优化任务与提示词索引

## 推荐执行顺序

### 第0阶段：建立基线（先执行）
- [总控执行协议与基线任务](prompts/master-execution-and-baseline.md)
- 目标：确认真实代码、测试、CI、性能和数据生命周期基线，不盲目改造。

### 第1阶段：测试与质量门禁
1. [IR层测试覆盖](prompts/task1-ir-testing.md)
   - Fuzzing、属性测试、跨协议 round-trip、IR生命周期文档
2. [CI流水线强化](prompts/task2-ci-enhancement.md)
   - race detector、静态分析、goleak
3. [IR测试增强](prompts/task4-ir-testing-enhancement.md)
   - 持续 fuzzing、属性不变量和协议兼容性测试

### 第2阶段：可观测性与运维
4. [错误展示优化](prompts/task3-error-display-optimization.md)
   - 错误趋势、异常告警、错误格式标准化
5. [文档与API完善](prompts/task5-documentation-enhancement.md)
   - 架构图、数据流、OpenAPI和Swagger

### 第3阶段：性能与系统演进
6. [性能、追踪、兼容性、迁移与容量](prompts/tasks6-10-long-term-optimization.md)
   - 性能基线与优化
   - OpenTelemetry和指标治理
   - 持续 fuzzing
   - URSM v2渐进迁移
   - 容量、保留策略和恢复演练

## 使用方式

将以下两部分合并交给执行代理：

1. `prompts/master-execution-and-baseline.md` 中的“通用执行协议”
2. 目标任务文件中的具体任务章节

代理完成后必须返回：
- 结果摘要
- 文件变更
- 测试命令和真实结果
- 风险与回滚
- 后续建议

## 当前建议

先执行第0阶段基线任务，再根据真实结果决定是否实施CI门禁或IR测试。规划文档中的路径、类型和代码片段均是执行意图示例，不能替代对当前仓库的读取和验证。
