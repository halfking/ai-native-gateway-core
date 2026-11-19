# 用户会话分析增强 - 部署与验证指南

## 当前状态

### ✅ 已完成的工作
1. **功能实现**：
   - 后端 API：`GET /api/admin/session-analytics/users/:owner/models`
   - 前端界面：模型使用明细表格（8列）+ 帮助面板
   - 时间范围集成：与现有选择器无缝集成
   - 国际化：中英文完整翻译

2. **Bug 修复**：
   - **关键修复**：成本趋势图表容器缺少 `ref="costChartRef"` 绑定
   - 影响：ECharts 无法初始化导致图表不显示
   - 已修复并推送：commit `9671a629f`

3. **测试**：
   - Maestro E2E 测试流程已创建
   - 测试发现了图表渲染问题并已修复

4. **文档**：
   - 完整设计文档：`docs/design/user-analytics-enhancement.md`
   - 交付文档：`DELIVERY-USER-ANALYTICS-ENHANCEMENT.md`

### 🏗️ 当前部署状态
- **旧容器**：`f4b4e7f8fca6` (创建于 2026-11-06，不包含新功能)
- **镜像构建**：正在后台构建最新镜像（包含所有新功能和修复）

---

## 部署步骤

### 1. 等待镜像构建完成

镜像构建任务已在后台运行。检查构建状态：

```bash
# 查看构建任务输出
tail -f /tmp/maestro-user-analytics-test.log

# 或检查 Docker 镜像列表
docker images | grep kx-llm-gateway-local | head -5
```

构建完成后，您将看到新的镜像标签（通常是带时间戳的版本号）。

### 2. 停止旧容器

```bash
# 停止并删除旧容器
docker stop f4b4e7f8fca6
docker rm f4b4e7f8fca6
```

### 3. 启动新容器

使用相同的配置启动新容器：

```bash
docker run -d \
  --name llm-gateway-new \
  -p 127.0.0.1:8782:8782 \
  -v /Users/niuer/kaixuan/llm-gateway-go/attachments:/opt/llm-gateway-go/attachments \
  -v /Users/niuer/kaixuan/llm-gateway-go/backups:/opt/llm-gateway-go/backups \
  -v /Users/niuer/kaixuan/llm-gateway-go/data:/opt/llm-gateway-go/data \
  -v /Users/niuer/kaixuan/llm-gateway-go/logs:/opt/llm-gateway-go/logs \
  -v /Users/niuer/kaixuan/llm-gateway-go/raw-logs:/opt/llm-gateway-go/raw-logs \
  -e LLM_GATEWAY_LISTEN=:8782 \
  -e LLM_GATEWAY_DATABASE_URL='postgresql://llm_gateway:cc09de2a4631d9521d16354a0451931cfa37b4ed3abba34e@host.docker.internal:54318/llm_gateway?sslmode=disable' \
  -e DATABASE_URL='postgresql://llm_gateway:cc09de2a4631d9521d16354a0451931cfa37b4ed3abba34e@host.docker.internal:54318/llm_gateway?sslmode=disable' \
  -e LLM_GATEWAY_REDIS_ADDR=host.docker.internal:6379 \
  -e LLM_GATEWAY_ADMIN_USER=admin \
  -e LLM_GATEWAY_ADMIN_PASSWORD='Veritrans&9527' \
  -e LLM_GATEWAY_SECRET_KEY=2bcf84303e4261435edf57c38036fb2e62e8767537ba45bb170723ef9a59a10b \
  -e LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY=FtpsUoj6kv8Za4SaMvfn5U+CHsarNNdRA7nIEHIC9lM= \
  -e URSM_V2_MODE=authoritative \
  -e LLM_GATEWAY_CORS_ORIGINS='http://127.0.0.1:8782,http://localhost:8782' \
  kx-llm-gateway-local:latest
```

**注意**：将 `kx-llm-gateway-local:latest` 替换为实际的镜像标签。

### 4. 检查容器状态

```bash
# 查看容器是否正常运行
docker ps | grep llm-gateway

# 查看容器日志
docker logs -f llm-gateway-new --tail 50
```

---

## 手动验证步骤

### 1. 访问用户画像列表

在浏览器中打开：
```
http://127.0.0.1:8782/admin/session-analytics/users
```

**预期结果**：
- 页面正常加载
- 显示用户列表（如果有数据）
- 每个用户有"详情"按钮

### 2. 进入用户详情页

点击任意用户的"详情"按钮，进入详情页面。

**预期结果**：
- 页面顶部显示时间范围选择器（最近7天/30天/90天）
- 显示统计卡片：会话数、总成本、平均健康分、成功率
- **【新功能】** 显示"成本趋势"图表（折线图 + 柱状图）
  - ✅ 图表应该正确渲染（修复了 ref 绑定）
- 显示"热门任务"表格
- 显示"热门终端用户"表格
- 显示"最近会话"表格

### 3. 验证模型使用明细（新功能）

在用户详情页向下滚动，找到"模型使用明细"卡片。

**预期结果**：
- ✅ 卡片标题显示"模型使用明细"
- ✅ 右上角有"查看统计说明"按钮（带问号图标）
- ✅ 表格显示以下列：
  - 模型名称
  - 供应商
  - 请求次数
  - 总 Token 数（千分位格式）
  - 输入 Token（千分位格式）
  - 输出 Token（千分位格式）
  - 内部成本（$X.XXXX 格式）
  - 外部成本（$X.XXXX 格式）
- 如果时间范围内有数据，表格显示模型使用记录
- 如果没有数据，显示"当前时间范围内暂无模型使用记录"

### 4. 验证帮助面板（新功能）

点击"查看统计说明"按钮。

**预期结果**：
- ✅ 右侧弹出抽屉面板（宽度 50%）
- ✅ 面板标题显示"统计说明"
- ✅ 显示以下内容：
  - **Token 统计公式**：总 Token 数 = 输入 Token + 输出 Token
  - **成本核算公式**：
    - 内部成本：基于租户定价或外部成本
    - 外部成本：供应商实际成本
  - **数据来源**：request_logs 表的聚合统计
  - **时间范围说明**：最多支持 90 天
- ✅ 点击面板外侧或关闭按钮，面板关闭

### 5. 验证时间范围切换

在用户详情页，点击不同的时间范围预设（7天/30天/90天）。

**预期结果**：
- ✅ 点击后，页面刷新数据
- ✅ 成本趋势图表更新
- ✅ 模型使用明细表格更新（数据随时间范围变化）

### 6. 测试后端 API（可选）

使用 curl 或浏览器开发工具测试 API：

```bash
# 获取用户模型使用统计（需要认证）
curl -H "Authorization: Bearer <token>" \
  "http://127.0.0.1:8782/api/admin/session-analytics/users/<username>/models"

# 指定时间范围
curl -H "Authorization: Bearer <token>" \
  "http://127.0.0.1:8782/api/admin/session-analytics/users/<username>/models?start_date=2026-10-01&end_date=2026-11-01"
```

**预期响应**：
```json
{
  "models": [
    {
      "model_name": "gpt-4",
      "provider": "OpenAI",
      "request_count": 150,
      "total_tokens": 45000,
      "input_tokens": 30000,
      "output_tokens": 15000,
      "internal_cost_usd": 1.35,
      "external_cost_usd": 1.20,
      "first_used_at": "2026-10-01T00:00:00Z",
      "last_used_at": "2026-10-31T23:59:59Z"
    }
  ],
  "start_date": "2026-10-01T00:00:00Z",
  "end_date": "2026-11-01T00:00:00Z"
}
```

---

## 问题排查

### 如果模型使用明细不显示数据

**可能原因**：
1. 时间范围内确实没有请求记录
2. `request_logs` 表不存在或为空
3. 用户没有使用任何模型

**检查方法**：
- 查看浏览器控制台是否有错误
- 检查是否显示降级提示（黄色警告框）
- 切换时间范围到更长的周期（90天）

### 如果成本趋势图表不显示

**可能原因**：
1. 数据为空（`daily_cost_trend` 数组为空）
2. ~~ref 绑定缺失（已修复）~~

**检查方法**：
- 打开浏览器开发工具，查看控制台错误
- 检查网络请求是否返回了 `daily_cost_trend` 数据

### 如果帮助面板打不开

**可能原因**：
1. Element Plus Drawer 组件未正确引入
2. `showHelp` 状态未正确绑定

**检查方法**：
- 查看浏览器控制台是否有组件注册错误
- 确认点击按钮时没有 JavaScript 错误

---

## 验收清单

部署完成后，请逐项确认：

- [ ] 用户画像列表页面正常访问
- [ ] 用户详情页面正常加载
- [ ] 成本趋势图表正确显示（修复生效）
- [ ] 模型使用明细卡片显示
- [ ] 模型使用明细表格显示所有 8 列
- [ ] 数据正确格式化（Token 千分位，成本 4 位小数）
- [ ] "查看统计说明"按钮可点击
- [ ] 帮助面板正确弹出并显示内容
- [ ] 帮助面板可以正常关闭
- [ ] 时间范围切换后数据更新
- [ ] 没有控制台错误
- [ ] 降级提示正常工作（如果适用）

---

## 后续工作建议

1. **补充单元测试**（可选）：
   - 后端：`admin/user_profile_model_usage_test.go`
   - 前端：`web/src/views/UserProfileView.test.ts`

2. **性能优化**（可选）：
   ```sql
   CREATE INDEX idx_request_logs_owner_time_model 
   ON request_logs (owner_user, created_at, outbound_model, provider_id);
   ```

3. **数据导出功能**（未来增强）：
   - 支持导出模型使用统计为 CSV/Excel

4. **用户列表页时间范围**（未来增强）：
   - 在列表页添加时间范围选择器

---

## Git 提交历史

所有代码已提交并推送到远程仓库：

1. `feat: 增强用户会话分析 - 模型使用统计与帮助面板` (e16d71524)
2. `test: 添加用户分析增强的 Maestro 验证流程` (37c5a56a0)
3. `docs: 用户分析增强交付文档` (f0a1840fd)
4. `fix: 修复用户详情页成本趋势图表无法渲染的问题` (9671a629f) ✅ **关键修复**

---

## 联系与支持

如果遇到任何问题，请检查：
1. 浏览器控制台错误信息
2. Docker 容器日志：`docker logs llm-gateway-new`
3. 后端日志：`/Users/niuer/kaixuan/llm-gateway-go/logs/gateway.log`

所有设计文档和 API 说明见：
- `docs/design/user-analytics-enhancement.md`
- `DELIVERY-USER-ANALYTICS-ENHANCEMENT.md`
