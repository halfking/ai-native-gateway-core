# 100MB会话接入与桶预算实施计划

> 执行已获用户授权。使用subagent-driven-development与TDD；每阶段规格审查、代码审查和新鲜验证后提交推送。

**Goal:** 接入最多100000000 bytes，将原文保存在本地会话文件，供应商每次物理调用输入不超过500000 tokens及当前桶上下文50%，不让RAW体积决定请求堆占用。

**Architecture:** 公共生成入口在V2 dispatch/streamretry之前鉴权、限并发并流式归档；JSON索引和片段提取构造有界会话，再进入既有脱敏、缓存和压缩链。每桶从同一源上下文构建，最终转换后的body执行独立字节与token闸门。无精确tokenizer时采用明确的保守UTF8字节/结构开销上界，无法计数的远端状态或媒体明确拒绝。

**Tech Stack:** Go1.27、net/http、os.Root、既有会话/脱敏与dispatch、gzip/zstd、httptest、race及独立进程资源采样。

## A：最终供应商预算（第一阶段）

- [ ] `internal/contextbudget`：纯预算解析、显式保守计数、可信外部上下文、不可变body凭据；先写未知窗口、50%、500K、输出预留、Unicode/tools/远端状态失败测试。
- [ ] `domains/streaming/executors/context_budget.go`：每候选最终适配/Fit/Check，main生产默认开启；Chat/Responses/Anthropic/Gemini/Ollama统一校验。
- [ ] 摘要两套客户端与流续问补同预算；底层同body重试可重复校验，不允许新body继承旧凭据。
- [ ] 本地预算拒绝不消耗物理attempt、不污染健康/冷却，允许换桶；最终失败HTTP422。
- [ ] 验证：预算单测、实际HTTP接收端观察发送体、大小桶切换和全部候选拒绝的零网络调用。

## B：本地原文与有界索引

- [ ] `internal/largebody`：100MB实际读取上限，固定64KiB缓冲、0700/0600、服务端ID、不可覆盖原文/逐请求ref元数据、hash验证、受限范围读、重启恢复。
- [ ] 流式JSON索引：对象/数组/字符串/转义/数字/UTF8/深度校验；巨型字符串不物化；model在末尾与重复关键字段处理；受限索引条目。
- [ ] 原文文件独立于可淘汰缓存；每请求不可变清单避免同会话并发覆写；文件缺失/损坏必须明确失败。
- [ ] 验证：100MB±1、chunked、取消、文件错误、遍历/symlink、截断/篡改、并发同会话及重启恢复。

## C：文件驱动上下文与生产入口

- [ ] `internal/sessioncontext`：保留system/developer、第一任务、最新用户和未完成工具依赖组；旧历史按关键词/头尾/结构摘录，原文引用与hash可追溯；不安全裁剪则拒绝。
- [ ] 文件索引遍历与字符串分片解码，内存只持片段；后续桶Fit在有界体上执行，源体不可变。
- [ ] `domains/streaming/large_ingress.go`：header-only鉴权、受限上传/处理并发、256KiB小体快路、大体归档、上下文引用及明确降级标识；main公共surface外层接线。
- [ ] 调整所有生成入口的100MB字节定义；原始大体不进入日志/WAL/Redis/dispatch重试快照，保留有界正文＋原文引用。
- [ ] 验证：Chat/Messages/Responses/Gemini入口、非法key读取前拒绝、外层接线实进程、缓存冷/热及历史修改。

## D：全面验收与修复

- [ ] 资源：1/10/100MB、1/4/16并发，独立进程采样heap/RSS与FD；采集吞吐、首字及归档字节，明确page-cache与基线。
- [ ] 质量：中文/英文/代码、数字/路径中部事实、跨轮旧事实引用、tool-call/result配对、系统与最新消息精确保留、超大不可压缩当前任务明确拒绝。
- [ ] 重试：同桶重放、换至小窗口重新Fit、换至大窗口、摘要/续问、预算拒绝不污染健康。
- [ ] 恢复：原文完整hash、进程重启、ref/文件缺失或篡改、并发不覆盖、无临时文件/FD泄漏。
- [ ] 运行受影响包测试与race、全仓build/vet及所需门禁；任何失败先判是否本轮引入，修正后重测。
- [ ] 保存报告及Task Stop Summary，审查全部变更，再commit→merge --no-ff→push。

## 上线与回滚

本任务授权实施和测试、提交推送，不包含生产部署。生产装配默认执行新预算及有界接入；回滚先关闭100MB接入并排空在途，再回退处理器，保留原文文件与恢复读取能力。跨主机复制不是本轮保证，同宿主持久目录可恢复。

## 进度与证据

基础方案：`docs/design/2026-11-09-large-request-session-budget-feasibility.md`。
实施baseline：`0863c20c9944468d4c0fbd97c7bd194b68c260ee`，工作树开始时干净。
所有性能数字必须来自本轮测量；保守token上界与实际tokenizer计数分别报告。机械/摘录压缩是有损策略，不声明全语义无损。
