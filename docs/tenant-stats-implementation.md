# 租户统计功能实施方案 - 完成总结

## 需求概述

1. **首页统计**：在 https://llmgo.kxpms.cn/ 首页显示当前租户的所有请求和费用统计
   - 当租户为 "default" 时，显示整站数据（所有租户汇总）
   - 当租户为其他时，显示该租户的数据

2. **密钥签发**：在 /keys 页面签发密钥时，非 default 租户的用户不能在弹窗中选择租户
   - 应该默认为当前租户且不能更改

3. **请求日志**：非 default 租户的 /request-logs 页面
   - 需要增加租户过滤条件
   - 只能查看最近 3 天的数据

## 实施完成

### 后端修改

#### 1. `admin/context.go`

新增 `EffectiveTenantIDAll` 函数：
- 对于 `tenant_admin`，返回用户自己的租户 ID
- 对于 `super_admin`，返回空字符串（表示查询所有租户）

#### 2. `admin/usage.go`

修改以下函数支持查询所有租户：
- `usageSummary` - 使用 `EffectiveTenantIDAll`，super_admin 看整站汇总
- `usageDashboard` - 动态构建 SQL，super_admin 不按租户过滤
- `usageHotKeys` - super_admin 看所有租户的热 Key
- `usageByProvider` - super_admin 看所有供应商统计
- `usageByModel` - super_admin 看所有模型统计
- `usageByKey` - super_admin 看所有 Key 统计

### 前端修改

#### 1. `store.ts`

新增判断函数：
- `isSuperAdmin()` - 判断当前用户是否为 super_admin
- `isDefaultTenant()` - 判断当前租户是否为 default
- `getCurrentTenantId()` - 获取当前租户 ID

#### 2. `DashboardView.vue`

- 导入 store 中的判断函数
- 新增 `tenantLabel` 计算属性，显示当前租户标识
- 在页面头部添加租户徽章显示

#### 3. `KeysView.vue`

- 导入 store 中的判断函数
- 修改 `openNew` 函数，非 default 租户默认使用当前租户
- 修改模板中的租户选择 input，非 default 租户时禁用
- 添加提示信息说明非 default 租户只能签发当前租户的密钥

#### 4. `RequestLogsView.vue`

- 导入 store 中的判断函数
- 新增 `tenantLabel` 计算属性
- 新增 `validateHours` 函数，非 default 租户限制最大 72 小时
- 修改时间范围 select，非 default 租户禁用 7 天选项
- 添加租户徽章显示
- 添加非 default 租户的时间限制提示

## 测试验证

### 编译测试

- ✅ 后端 Go 编译通过 (`go build ./...`)
- ✅ 前端 TypeScript 编译通过 (`npm run build`)

### 功能验证清单

1. **首页统计**
   - [ ] super_admin 登录后，首页显示"整站数据"标签
   - [ ] super_admin 看到的统计数据包含所有租户的汇总
   - [ ] tenant_admin 登录后，首页显示"租户: xxx"标签
   - [ ] tenant_admin 看到的统计数据只包含当前租户

2. **密钥签发**
   - [ ] super_admin 签发密钥时可以自由选择租户
   - [ ] tenant_admin 签发密钥时租户字段默认为当前租户且禁用
   - [ ] tenant_admin 签发的密钥属于当前租户

3. **请求日志**
   - [ ] super_admin 可以查看所有租户的请求日志
   - [ ] super_admin 可以选择 1 小时、6 小时、24 小时、3 天、7 天
   - [ ] tenant_admin 只能查看当前租户的请求日志
   - [ ] tenant_admin 只能选择 1 小时、6 小时、24 小时、3 天
   - [ ] tenant_admin 无法选择 7 天选项
   - [ ] 页面显示租户标识和时间限制提示

## 修改的文件

### 后端
- `services/llm-gateway-go/admin/context.go` - 新增 `EffectiveTenantIDAll` 函数
- `services/llm-gateway-go/admin/usage.go` - 修改 6 个统计函数支持全租户查询

### 前端
- `services/llm-gateway-go/web/src/store.ts` - 新增 3 个判断函数
- `services/llm-gateway-go/web/src/views/DashboardView.vue` - 显示租户标识
- `services/llm-gateway-go/web/src/views/KeysView.vue` - 禁用非 default 租户选择
- `services/llm-gateway-go/web/src/views/RequestLogsView.vue` - 添加租户过滤和时间限制

## 注意事项

1. 后端 `EffectiveTenantIDAll` 返回空字符串时，SQL 查询不按 `tenant_id` 过滤，实现整站数据查询
2. 前端使用 `isDefaultTenant()` 判断是否为 default 租户，非 default 租户有以下限制：
   - 密钥签发时租户字段禁用
   - 请求日志时间范围最大 3 天
3. 所有修改都保持向后兼容，不影响现有功能
