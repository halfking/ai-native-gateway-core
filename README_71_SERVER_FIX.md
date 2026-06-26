# 71服务器问题修复快速指南

## 问题现象
- 无法通过 llmgateway.internal.example.com/v1 发起请求
- 路由无法匹配凭据
- 184数据库 request_logs 大量 empty_response

## 立即执行（在71服务器上）

### 1. 诊断问题
```bash
cd /path/to/llm-gateway-go-2
./scripts/diagnose_routing_issue.sh minimax-m3
```

### 2. 检查代码版本
```bash
git log --oneline -1
# 期望看到: 78de1295 fix(relay): resolve empty_response misclassification
```

### 3. 如果代码不是最新，更新并重启
```bash
git fetch origin
git checkout server-71
git pull origin server-71
go build -o llm-gateway ./cmd/gateway
systemctl restart llm-gateway
```

### 4. 测试修复
```bash
./scripts/test_routing_fix.sh
```

## 详细说明

请查看：`docs/HOTFIX_71_SERVER_ROUTING.md`

## 紧急联系

如果问题未解决，请立即执行诊断脚本并发送输出。
