# D01 — IR 生命周期

> 域知识库：[docs/audit/playbook/domains/D01-ir-lifecycle.md](../../../docs/audit/playbook/domains/D01-ir-lifecycle.md)  
> 48h 改动面（截至 R56）：§见末尾 §改动面  
> 状态：草稿

## 1. 审计要点（来自 playbook 域文档）

- IR 字段完整：多轮对话、路由数据、流程跟踪、调度瀑布、压缩脱敏、附件/媒体、日期/项目/用户/任务/轮次/多 tag/模型/总轮次/供应商/凭据
- 创建/分步赋值/解析/存储/序列化与反序列化全链路无损
- 内存缓存、队列 metadata 缓存、服务器目录文件缓存、附件/媒体存储、数据库存储、压缩存储协同一致
- 多轮会话双向解析 + 每轮摘要可读
- 出厂参数（超出厂商规范的）缓存 TTL + 返回时回填

## 2. 业务测试（business/）

- [ ] B-01：IR 必填字段（Model / Messages / Stream）在四种协议（openai / anthropic / responses / gemini）下序列化/反序列化后值守恒 —— `business/test_ir_field_roundtrip.go`
- [ ] B-02：extra_params 缓存 TTL 内的回写 —— `business/test_extra_params_ttl.go`
- [ ] B-03：请求侧标准名自动补录（B3 死路径修复 f7f8f66d5）—— `business/test_resolve_stdname.go`

## 3. 数据测试（data/）

- [ ] D-01：IR JSON golden 对拍（10 例多协议矩阵）—— `data/test_ir_golden.go`
- [ ] D-02：sanitize → compress → 还原 identity 映射不丢 occurrence —— `data/test_sanitize_compress_identity.go`
- [ ] D-03：sanitized IR 与 AlignmentMap 落库一致性 —— `data/test_alignment_map_roundtrip.go`

## 4. 压力测试（stress/）

- [ ] S-01：1000 req/s 下 IR 池化复用，alloc/req 衰减率 ≥80% —— `stress/bench_ir_pool.go`
- [ ] S-02：50 并发下 IR serialize P99 ≤10ms —— `stress/test_serialize_p99.go`

## 5. 安全测试（safety/）

- [ ] SF-01：超大 prompt（≥128k token）不 panic，序列化失败有结构化错误 —— `safety/test_oversized_prompt.go`
- [ ] SF-02：-race 模式下并发 IR 修改无 data race —— `safety/test_race_concurrent.go`
- [ ] SF-03：JSON 解析畸形不 panic、不泄漏内部栈 —— `safety/test_malformed_json.go`

## 6. 验收门

```bash
go build ./...
go vet ./...
go test -race -timeout 120s ./tests/48h-audit/D01-ir-lifecycle/business/...
go test -race -timeout 120s ./tests/48h-audit/D01-ir-lifecycle/data/...
go test -race -timeout 240s ./tests/48h-audit/D01-ir-lifecycle/stress/...
go test -race -timeout 120s ./tests/48h-audit/D01-ir-lifecycle/safety/...
bash tests/48h-audit/D01-ir-lifecycle/scripts/run.sh
```

## 7. 与方案文档的对齐

- 主 RFC：docs/架构优化v6/09-ir-class-journal-decoupling.md
- 三层缓存 RFC：docs/audit/2026-08-28-deep-audit.md §7
- 上轮挂账（R55）：docs/audit/playbook/runs/R54-2026-09-22/agent-D03-D05-cache-compression.md

## 改动面（截至 R56，48h 窗口）

- `internal/ir/parse_ollama_stream.go`（新增）：Ollama 流式解析（Wave5）
- `internal/ir/serialize_openai_type_switch_test.go`：Wave5 请求方向 GAP 闭合
- `errorsx/error_mapping_doctest_test.go`：errorsx 文档化
- 其它 IR 模块无 48h 改动，但作为长期 invariant 校验

## 子代理派发提示词

```
你是只读 + 可写 tests/48h-audit/D01-ir-lifecycle/ 的 worker 子代理。
知识库入口：docs/audit/playbook/domains/D01-ir-lifecycle.md
48h 改动面：internal/ir/parse_ollama_stream.go 等
必填：
  - plan.md §1-§7（基于上述模板填充）
  - business/test_ir_field_roundtrip.go：4 协议 × IR 必填字段 roundtrip
  - data/test_ir_golden.go：10 例多协议 golden
  - stress/bench_ir_pool.go 或 test_serialize_p99.go
  - safety/test_oversized_prompt.go + test_race_concurrent.go
  - reports/latest.md：本轮发现 + 钉桩
输出 ≤5KB。
```