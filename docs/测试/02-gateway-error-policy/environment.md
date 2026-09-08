# 02 网关错误策略 — 环境

- 代码基线：`origin/main` @ `493c1b34b`
- 工作树：`llm-gateway-go-4-error-policy` 分支 `fix/gateway-error-policy-logging`
- 本地 `~/kaixuan/llm-gateway-go`：**只读日志，不部署、不重启**
- 部署目标：154 `llm.kxpms.cn`，走仓库部署脚本
- 密钥：`env-injector inject aliyun-gateway-154`，禁止硬编码
