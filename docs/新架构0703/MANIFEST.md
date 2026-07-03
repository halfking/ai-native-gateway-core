# 新架构0703 - 文档清单

> **生成时间**: 2026-07-03  
> **架构版本**: v1.0  
> **状态**: 设计中

---

## 📚 文档结构

### 核心文档
- [x] `00-总体架构设计.md` - 架构总览
- [x] `phase1/README.md` - Phase 1概述
- [x] `phase1/module-1.1-hook-framework.md` - Hook框架详细设计
- [ ] `phase1/module-1.3-memora-auto.md` - Memora自动沉淀详细设计
- [ ] `phase2/README.md` - Phase 2概述
- [ ] `phase2/module-2.1-session-ingest-api.md` - 会话接收API
- [ ] `phase2/module-2.2-llm-extraction.md` - LLM智能提取
- [ ] `API-CONTRACT.yaml` - OpenAPI 3.0规范
- [ ] `SAMPLE-DATA.md` - 样例数据说明

### 开发提示词
- [ ] `prompts/phase1-module1.1.md` - Hook框架开发提示词
- [ ] `prompts/phase1-module1.3.md` - Memora插件开发提示词
- [ ] `prompts/phase2-module2.1.md` - 会话API开发提示词

### 样例数据
- [ ] `samples/session-samples.json` - 会话样例（10个场景）
- [ ] `samples/api-examples.yaml` - API请求响应示例
- [ ] `samples/knowledge-graph.json` - 知识图谱样例

---

## 🎯 待生成文档列表

### Phase 1 模块
1. module-1.2-output-sanitizer.md - 输出脱敏
2. module-1.3-memora-auto.md - Memora自动沉淀 ⭐
3. module-1.4-session-editor.md - 会话编辑器
4. module-1.5-vibe-coding.md - Vibe Coding评估

### Phase 2 模块
1. module-2.1-session-ingest-api.md - 会话接收API ⭐
2. module-2.2-llm-extraction.md - LLM智能提取 ⭐
3. module-2.3-cross-session-aggregator.md - 跨会话聚合
4. module-2.4-graph-builder.md - 知识图谱构建
5. module-2.5-auto-tagger.md - 自动标签

### Phase 3 模块
1. module-3.1-provider-profiler.md - 供应商画像
2. module-3.2-cross-summary-service.md - 跨会话总结服务

### Phase 4 模块
1. module-4.1-session-visualizer.md - 会话可视化
2. module-4.2-provider-dashboard.md - 供应商仪表盘
3. module-4.5-knowledge-graph-view.md - 知识图谱可视化

### API与数据
1. API-CONTRACT.yaml - 完整OpenAPI规范 ⭐
2. DATA-MODELS.md - 数据模型详细定义
3. SAMPLE-DATA.md - 样例数据说明 ⭐

---

## 🚀 文档生成优先级

### P0 (立即生成)
- [x] 总体架构设计
- [x] Hook框架详细设计
- [ ] Memora自动沉淀详细设计
- [ ] 会话接收API详细设计
- [ ] API契约规范
- [ ] 样例数据集

### P1 (本周生成)
- [ ] 所有Phase 1模块文档
- [ ] 所有Phase 2模块文档
- [ ] 开发提示词

### P2 (下周生成)
- [ ] Phase 3-5模块文档
- [ ] 集成测试文档
- [ ] 部署手册

---

## 📝 文档模板

每个模块文档应包含：
1. ✅ 模块概述（目标、范围）
2. ✅ 架构设计（组件图、数据流）
3. ✅ 数据结构（完整Go/Python代码）
4. ✅ API接口（请求/响应示例）
5. ✅ 验收标准（功能、性能、测试）
6. ✅ 测试用例（单元测试、集成测试）
7. ✅ 开发提示词（AI辅助）
8. ✅ 参考资料

---

## 📊 进度统计

- 总文档数: 25+
- 已完成: 3 (12%)
- 进行中: 1 (4%)
- 待开始: 21 (84%)

---

**维护人**: 架构组  
**更新频率**: 每日更新
