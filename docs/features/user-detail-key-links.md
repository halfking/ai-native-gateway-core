# 用户详情抽屉：高频密钥可点击跳转

## 概述

2026-11-17 实现用户详情抽屉中的"高频密钥"显示为可点击链接，点击后跳转到租户详情页的 Keys 标签页并高亮对应密钥行。

## 改动范围

### 1. 后端（无改动）

后端 API `/api/admin/users/:id/stats` 已经返回 `top_keys` 数组，每项包含：
```json
{
  "id": 123,
  "name": "key-alias",
  "requests": 1000
}
```

### 2. 前端 - UserDetailDrawer.vue

#### 新增函数
```typescript
function keyHref(keyId: number): string {
  if (!user.value) return '#'
  return `/tenants/${user.value.tenant_id}?tab=keys&key_id=${keyId}`
}
```

#### 模板改动
- **原来**：`<span class="mono">{{ topKeysText }}</span>`（纯文本）
- **现在**：分情况渲染
  - 如果没有密钥：显示 `—`
  - 如果有钻取权限（`canDrillKeys`）：显示可点击链接
    - 有 `id` 的密钥：`<a :href="keyHref(k.id)">name×requests</a>`
    - 无 `id` 的密钥：`<span class="mono">name×requests</span>`（降级为文本）
  - 如果没有钻取权限：显示纯文本（`topKeysText`）

#### 新增样式
```css
.udd-key-links { display: inline; }
.udd-key-sep { color: var(--muted); }  /* 密钥间的分隔符 · */
```

### 3. 前端 - TenantDetailView.vue

#### 新增状态
```typescript
const highlightKeyId = ref<number | null>(null)
```

#### 读取 URL 参数
在 `onMounted` 中添加：
```typescript
if (typeof route.query.key_id === 'string') {
  const kid = Number.parseInt(route.query.key_id, 10)
  if (!Number.isNaN(kid)) {
    highlightKeyId.value = kid
    // 高亮 3 秒后自动清除
    setTimeout(() => { highlightKeyId.value = null }, 3000)
  }
}
```

#### 模板改动
Keys 表格的行添加动态 class：
```vue
<tr v-for="k in sortedVisibleKeys" :key="k.id" 
    :class="{ 'highlight-row': highlightKeyId === k.id }">
```

#### 新增样式
```css
.highlight-row {
  background-color: color-mix(in srgb, var(--accent) 15%, transparent);
  animation: highlight-fade 3s ease-out;
}
@keyframes highlight-fade {
  0% { background-color: color-mix(in srgb, var(--accent) 25%, transparent); }
  100% { background-color: transparent; }
}
```

## 用户体验

1. **用户详情抽屉**
   - 打开任意用户的详情抽屉
   - "高频密钥"行显示可点击的密钥链接（蓝色，带悬停效果）
   - 多个密钥用 ` · ` 分隔

2. **点击跳转**
   - 点击任意密钥链接
   - 自动跳转到该用户所属租户的详情页
   - 自动切换到 Keys 标签页
   - 目标密钥行以淡蓝色高亮显示
   - 高亮效果 3 秒后自动淡出

3. **边界情况**
   - 如果密钥没有 `id`（理论上不应该发生）：显示为纯文本，不可点击
   - 如果用户没有密钥：显示 `—`
   - 如果当前用户没有查看租户密钥的权限（`canDrillKeys` 为 false）：显示纯文本而非链接

## 技术细节

### URL 结构
```
/tenants/{tenant_id}?tab=keys&key_id={key_id}
```

### 高亮机制
- 使用 CSS `color-mix()` 生成半透明的强调色背景
- 使用 CSS 动画从 25% 透明度淡出到完全透明
- 3 秒后通过 JavaScript 清除高亮状态（避免导航后仍保留高亮）

### 权限控制
继承现有的 `canDrillKeys` 逻辑：
- 如果用户有查看租户密钥的权限：渲染链接
- 如果没有权限：降级为纯文本显示

## 验证

### 手工验证步骤
1. 登录管理后台
2. 进入用户列表（`/users`）
3. 点击任意用户的详情按钮（眼睛图标）
4. 在详情抽屉中查看"高频密钥"行
5. 点击任意密钥链接
6. 验证：
   - 跳转到正确的租户详情页
   - Keys 标签页已激活
   - 目标密钥行高亮显示
   - 3 秒后高亮淡出

### 自动化验证
```bash
cd web
node /tmp/test_user_detail_keys.js
```

检查项：
- ✓ UserDetailDrawer 有 `keyHref` 函数
- ✓ `keyHref` 使用 `key_id` 参数
- ✓ 模板使用 `keyHref` 生成链接
- ✓ TenantDetailView 有 `highlightKeyId` 状态
- ✓ 从 `route.query.key_id` 读取参数
- ✓ 表格行应用 `highlight-row` 类
- ✓ 存在高亮样式和动画

### 构建验证
```bash
cd web
npm run build  # ✓ 构建成功
npx vue-tsc --noEmit  # ✓ 类型检查通过
npm run test  # ✓ 相关测试通过（已有的失败与本改动无关）
```

## 未来增强

可选的改进方向：

1. **滚动到视口**：当高亮的密钥行不在可见区域时，自动滚动到该行
2. **URL 历史管理**：使用 `router.replace` 在 3 秒后移除 `key_id` 参数（避免刷新页面时重复高亮）
3. **加载态提示**：如果密钥列表尚未加载，显示加载指示器
4. **深色模式适配**：验证高亮颜色在深色主题下的可见性

## 相关文件

- `web/src/components/UserDetailDrawer.vue` - 用户详情抽屉
- `web/src/views/TenantDetailView.vue` - 租户详情页
- `web/src/api/users.ts` - 用户 API（已有 `getUserStats` 接口）
- `admin/users_stats.go` - 后端统计接口（已返回 `top_keys`）
