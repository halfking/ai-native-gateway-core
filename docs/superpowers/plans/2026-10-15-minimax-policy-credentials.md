# MiniMax Policy Credentials Implementation Plan

> **For agentic workers:** Execute the approved repair inline task-by-task with test-driven-development; use independent code-review agents before final integration.

**Goal:** 修复合法脱敏工具调用误拦，扩展人工凭据声明识别，补齐失败原因并提交推送。
**Architecture:** 底层共享凭据语法视图；检测器仍负责类型与位置；输出守卫仍负责阻断；协议与还原链保持职责。
**Tech Stack:** Go、regexp、现有SSE回放与Go测试，无新依赖。

- [x] 1. `internal/credentialtext`：先写sed通配/替换/混合脚本回归，验证红，再实现偏移不变的检测视图；使真实替换值继续检测。
- [x] 2. `security/sanitize/detector.go`、`domains/outputcompliance/checker.go`：接入视图；生产YAML与fallback、1971字节工具样本回放；真实秘密/公网/无关规则反向验证。
- [x] 3. `security/sanitize/operational_patterns.go`、`detector_config.go`、`configs/sensitive_patterns.yaml`：按设计扩展声明与组合模式；每一模式先失败再实现，校验值边界和原文还原。
- [x] 4. `security/sanitize/output_sensitive.go`、`domains/streaming/handler.go`：结构化无秘密原因与终态分类；协议级工具参数及survival审计测试先红后绿。
- [x] 5. 撤修复变异、包全测/race、全仓vet/build、统一门与pristine基线对照；审计所有变更并更新LESSONS。
- 收口流程： 每个独立片段通过验证和精确SHA审查后小步commit、merge --no-ff main、push origin main；最后确认远端SHA并清理本任务worktree。

执行命令：`go test ./internal/credentialtext ./security/sanitize ./domains/outputcompliance ./domains/hooks/outputcompliance ./domains/streaming ./domains/streaming/executors -count=1 -timeout=300s`；`go test -race`同目标；`CGO_ENABLED=0 go vet ./...`；`CGO_ENABLED=0 go build ./...`；`bash verify.sh`。
验证日志存本机临时目录；提交不含真实凭据或生产会话正文。
