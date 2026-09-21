# 01 自检及时恢复 — 环境

- 仓库：`llm-gateway-go` worktree `fix/selfcheck-timely-recovery`
- 运行：`go test ./bg ./errorsx -count=1`
- 不需要 Postgres / Redis / 浏览器
- 不读写生产 252 / 154
