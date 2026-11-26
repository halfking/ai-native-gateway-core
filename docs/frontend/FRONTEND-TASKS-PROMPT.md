# 前端开发任务提示词

> 自动生成于 2026-11-26，基于 W2+W4 后端已完成的交付成果

---

## 任务1: W2.4 实现Schema驱动的插件设置表单

### 背景
后端已实现Schema生成器（从Go struct tag自动生成JSON Schema），现需前端实现SchemaForm组件，让插件配置页无需手写Vue代码即可自动渲染。

### 核心需求
创建一个通用的 `<SchemaForm>` 组件，根据JSON Schema自动渲染表单控件：
- 支持类型：boolean（Switch）、integer（InputNumber）、string（Input）、enum（Select）、uri（Input with icon）
- 自动验证：required、minimum、maximum、enum、format
- UI特性：字段说明提示、分组显示、实时验证

### API端点（后端已实现）
```
GET /api/admin/plugins/{name}/manifest  → 获取包含settings_schema的manifest
GET /api/admin/plugins/{name}/settings  → 获取当前配置值
PUT /api/admin/plugins/{name}/settings  → 保存配置（后端自动验证schema）
POST /api/admin/plugins/{name}/settings/reset → 重置为默认值
```

### Schema示例
```json
{
  "type": "object",
  "properties": {
    "enable_audit": {
      "type": "boolean",
      "title": "启用审计日志",
      "description": "记录所有会话操作",
      "default": true
    },
    "retention_days": {
      "type": "integer",
      "title": "日志保留天数",
      "minimum": 7,
      "maximum": 365,
      "default": 90
    },
    "export_format": {
      "type": "string",
      "title": "导出格式",
      "enum": ["json", "csv", "parquet"],
      "default": "json"
    }
  },
  "required": ["enable_audit", "retention_days"]
}
```

### 组件接口
```vue
<SchemaForm
  :schema="pluginSchema"
  v-model="currentSettings"
  @submit="onSave"
  :readonly="false"
/>
```

### 验收标准
- [ ] 根据schema.type自动选择表单控件（Switch/InputNumber/Select/Input）
- [ ] 验证规则自动生成（required/minimum/maximum/enum）
- [ ] schema.description显示为字段说明
- [ ] 保存时前端验证通过后调用PUT接口
- [ ] 重置按钮恢复schema.default值
- [ ] 至少2个插件（ASM + Forensics）配置页正常工作

### 参考文档
完整实现细节见：`llm-gateway-go/docs/frontend/W2.4-SCHEMA-FORM-SPEC.md`（18KB，包含完整代码示例）

### 预估工期
4-5天（SchemaForm组件复杂度较高，需支持多种类型和验证规则）

---

## 任务2: W4.2 实现API Key能力勾选UI

### 背景
后端已实现能力权限门（capability gate），API Key可限制使用的能力（audit/forensics/summary等），现需前端在Key创建/编辑界面增加能力勾选功能。

### 核心需求
在API Key管理页面增加：
1. **创建Key表单**：增加"授权能力"勾选框（按category分组显示）
2. **Key列表页**：显示每个Key的能力标签
3. **Key详情页**：显示已授权能力，支持编辑

### API端点（后端已实现）
```
GET /api/admin/capabilities → 获取能力清单（6个capability + 分类）
POST /api/admin/keys → 创建Key（body包含capabilities字段）
GET /api/admin/keys/{id} → 获取Key详情（包含capabilities）
PATCH /api/admin/keys/{id}/capabilities → 更新Key的能力
```

### 能力清单示例
```json
{
  "capabilities": [
    {
      "id": "audit",
      "name": "审计日志",
      "description": "查询请求日志、操作记录",
      "category": "security"
    },
    {
      "id": "forensics",
      "name": "取证导出",
      "description": "导出完整请求响应、指纹数据",
      "category": "security"
    },
    {
      "id": "summary",
      "name": "会话摘要",
      "description": "生成会话摘要、关键点提取",
      "category": "analytics"
    }
  ]
}
```

### UI设计要点
```vue
<!-- Key创建表单 -->
<el-form-item label="授权能力">
  <el-checkbox-group v-model="form.capabilities">
    <div class="capability-group">
      <div class="group-title">安全能力</div>
      <el-checkbox label="audit">审计日志 <el-tooltip>...</el-tooltip></el-checkbox>
      <el-checkbox label="forensics">取证导出 <el-tooltip>...</el-tooltip></el-checkbox>
    </div>
    <div class="capability-group">
      <div class="group-title">分析能力</div>
      <el-checkbox label="summary">会话摘要 <el-tooltip>...</el-tooltip></el-checkbox>
    </div>
  </el-checkbox-group>
</el-form-item>

<!-- Key列表 -->
<el-table-column label="授权能力">
  <template #default="{ row }">
    <el-tag v-for="cap in row.capabilities" size="small">{{ capabilityName(cap) }}</el-tag>
  </template>
</el-table-column>
```

### 验收标准
- [ ] Key创建表单显示能力勾选框，按category分组
- [ ] 能力说明以Tooltip显示
- [ ] Key列表显示能力标签
- [ ] Key详情页显示已授权能力，支持编辑
- [ ] 创建Key时携带capabilities字段
- [ ] 权限测试：未授权能力的Key调用端点返回403

### 参考文档
完整实现细节见：`llm-gateway-go/docs/frontend/W4.2-CAPABILITY-KEY-SPEC.md`（13KB，包含完整代码示例）

### 预估工期
2-3天（UI相对简单，主要是勾选框和标签显示）

---

## 任务3: W4.3 实现渠道商/客户视角门户

### 背景
后端已实现渠道商（channel）→ 客户（customer）→ 租户的层级关系和4个API端点，现需前端实现S2渠道商门户，让渠道商查看自己名下客户的会话和成本统计。

### 核心需求
创建两个页面：
1. **渠道商仪表盘**：显示渠道整体统计 + 客户列表
2. **客户详情页**：显示客户的会话统计 + 会话列表

### API端点（后端已实现）
```
GET /api/channels/{id}/customers → 渠道客户列表
GET /api/channels/{id}/stats → 渠道整体统计（客户数/会话数/月度成本）
GET /api/channels/{id}/customers/{customer_id}/stats → 客户统计
GET /api/channels/{id}/customers/{customer_id}/sessions → 客户会话列表
```

### 权限模型
- **渠道用户**（role='channel'）：只能查看归属自己的渠道和客户
- **super_admin**：可查看任意渠道/客户
- **跨渠道访问**：返回403 Forbidden

### 页面结构
```vue
<!-- 渠道商仪表盘 -->
<div class="channel-dashboard">
  <!-- 统计卡片 -->
  <el-row :gutter="20">
    <el-col :span="8">
      <MetricCard title="客户总数" :value="stats.total_customers" />
    </el-col>
    <el-col :span="8">
      <MetricCard title="会话总数" :value="stats.total_sessions" />
    </el-col>
    <el-col :span="8">
      <MetricCard title="月度成本" :value="stats.monthly_cost_usd" unit="USD" />
    </el-col>
  </el-row>

  <!-- 客户列表 -->
  <el-table :data="customers" @row-click="goToCustomerDetail">
    <el-table-column prop="name" label="客户名称" />
    <el-table-column prop="code" label="客户代码" />
    <el-table-column prop="created_at" label="创建时间" />
  </el-table>
</div>

<!-- 客户详情页 -->
<div class="customer-detail">
  <!-- 客户统计 -->
  <el-row :gutter="20">
    <el-col :span="8">
      <MetricCard title="会话总数" :value="customerStats.total_sessions" />
    </el-col>
    <el-col :span="8">
      <MetricCard title="活跃用户" :value="customerStats.active_users" />
    </el-col>
    <el-col :span="8">
      <MetricCard title="月度成本" :value="customerStats.monthly_cost_usd" unit="USD" />
    </el-col>
  </el-row>

  <!-- 会话列表 -->
  <el-table :data="sessions">
    <el-table-column prop="identity_hash" label="会话ID" />
    <el-table-column prop="last_activity" label="最后活动" />
    <el-table-column prop="request_count" label="请求数" />
    <el-table-column prop="total_cost_usd" label="成本" />
  </el-table>
</div>
```

### 路由配置
```javascript
{
  path: '/channels/:id',
  component: ChannelLayout,
  meta: { requiresAuth: true, roles: ['channel', 'super_admin'] },
  children: [
    { path: '', name: 'ChannelDashboard', component: ChannelDashboard },
    { path: 'customers/:customerId', name: 'CustomerDetail', component: CustomerDetail }
  ]
}
```

### 错误处理
- **403 Forbidden**：显示"无权访问该客户"，重定向到渠道主页
- **401 Unauthorized**：重定向到登录页
- **400 Bad Request**：显示"参数错误"

### 验收标准
- [ ] 渠道商仪表盘显示整体统计和客户列表
- [ ] 点击客户跳转到详情页
- [ ] 客户详情页显示统计卡片和会话列表
- [ ] 权限测试：渠道A用户无法访问渠道B的数据（403）
- [ ] 权限测试：super_admin可访问任意渠道
- [ ] 数据时效：统计数据为近30天

### 参考文档
完整实现细节见：`llm-gateway-go/docs/frontend/W4.3-CHANNEL-API-SPEC.md`（8KB，包含完整代码示例）

### 预估工期
3-5天（需实现2个页面 + 权限验证 + 后端联调）

---

## 通用开发环境准备

### 1. 克隆仓库
```bash
# 后端仓库（查看API实现和文档）
git clone codeup.aliyun.com:kaixuan/official-deploy/llm-gateway-go.git

# 前端仓库（假设路径）
git clone <前端仓库地址>
```

### 2. 启动后端（本地联调）
```bash
cd llm-gateway-go

# 运行迁移（创建新表）
# 迁移492: channels/customers/channel_users表
# 迁移20261125000001: api_keys.capabilities列

# 启动网关
go run cmd/gateway/main.go
```

### 3. 启动前端
```bash
cd llm-gateway-fe  # 实际路径可能不同

npm install
npm run dev
```

### 4. 种子数据（测试用）
```sql
-- 渠道商测试数据
INSERT INTO channels (name, code, contact) VALUES ('测试渠道A', 'channel-a', 'test@example.com');
INSERT INTO customers (name, code, channel_id) VALUES ('客户A1', 'customer-a1', 1);
INSERT INTO users (tenant_id, username, password_hash, role) VALUES ('default', 'channel_user', '<hash>', 'channel');
INSERT INTO channel_users (user_id, channel_id) VALUES (1, 1);

-- 能力测试数据（系统预置，无需插入）
-- 能力清单由后端自动生成

-- 插件测试数据（ASM manifest已包含settings_schema）
-- 通过 GET /api/admin/plugins/ai-session-manager/manifest 验证
```

---

## 技术栈要求

- **Vue 3** + Composition API (setup script)
- **Element Plus** 组件库
- **TypeScript**（推荐）
- **Vue Router** 路由管理
- **Axios** / **fetch** HTTP请求

---

## 联调检查点

### W2.4联调
- [ ] GET /api/admin/plugins/ai-session-manager/manifest 返回settings_schema
- [ ] GET /api/admin/plugins/ai-session-manager/settings 返回当前配置
- [ ] PUT /api/admin/plugins/ai-session-manager/settings 更新成功
- [ ] 违反schema验证规则返回400 + 字段错误信息

### W4.2联调
- [ ] GET /api/admin/capabilities 返回6个能力
- [ ] POST /api/admin/keys 包含capabilities字段创建成功
- [ ] GET /api/admin/keys/{id} 返回capabilities数组
- [ ] PATCH /api/admin/keys/{id}/capabilities 更新成功

### W4.3联调
- [ ] GET /api/channels/1/customers 返回客户列表
- [ ] GET /api/channels/1/stats 返回渠道统计
- [ ] GET /api/channels/1/customers/1/stats 返回客户统计
- [ ] GET /api/channels/1/customers/1/sessions 返回会话列表
- [ ] 跨渠道访问返回403

---

## 提交规范

### 分支策略
```bash
# 为每个任务创建feature分支
git checkout -b feature/w2.4-schema-form
git checkout -b feature/w4.2-capability-key-ui
git checkout -b feature/w4.3-channel-portal
```

### 提交信息
```
feat(plugin): 实现Schema驱动插件设置表单

- 实现SchemaForm通用组件
- 支持boolean/integer/string/enum/uri类型
- 自动表单验证（required/minimum/maximum）
- ASM/Forensics插件配置页集成

Closes #<issue-number>
```

### Code Review清单
- [ ] 组件可复用（SchemaForm支持多种schema）
- [ ] 错误处理完善（网络错误、验证失败、权限不足）
- [ ] UI响应式（适配不同屏幕）
- [ ] 国际化支持（中文文案可切换英文）
- [ ] 单元测试覆盖核心逻辑

---

## 技术支持

- **后端API规格**：见各任务的参考文档（`docs/frontend/`目录）
- **后端联系人**：见git提交记录（`git log --oneline --grep="W2\|W4"`）
- **问题反馈**：创建Issue，标注 `frontend` 标签

---

## 优先级建议

1. **先做W4.2**（2-3天，最简单）：验证前后端联调流程
2. **再做W4.3**（3-5天，中等）：熟悉权限验证逻辑
3. **最后做W2.4**（4-5天，最复杂）：通用组件设计考验

---

**文档生成时间**: 2026-11-26 18:02  
**后端最新提交**: `f9cfa052d`  
**总预估工期**: 9-13天（串行开发）或 5-7天（3人并行）
