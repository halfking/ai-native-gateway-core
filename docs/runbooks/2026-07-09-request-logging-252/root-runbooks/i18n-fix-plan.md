# i18n TODO 标记修复计划

## 问题分析

项目中存在 3058 个 `[TODO: ...]` 标记，分散在 8 个语言文件中。这些标记是由 `i18n-fix.mjs` 脚本自动生成的占位符，从未被清理或翻译。

### 三种类型的 TODO 标记

1. **真正缺失的翻译** - Vue 组件引用的键在所有语言文件中都缺失（如 `login.changePassword`）
2. **未使用的嵌套翻译** - 正确翻译在嵌套结构中，但 Vue 组件使用扁平路径（如 `chat.page.auto` 应移动到 `chat.auto`）
3. **冗余的根级别 TODO** - 正确翻译已存在于嵌套结构中，根级别 TODO 是多余的（如 `providerDetail.clearBtn`）

## 修复策略

### 策略选择：移动翻译到根级别（用户选择）

对于 Vue 组件使用扁平路径的情况，将嵌套结构中的正确翻译移动到根级别，保持 Vue 组件代码不变。

### 修复步骤

#### 步骤 1：识别所有需要修复的文件

需要修复的文件（8 个语言 × 15+ 个模块文件）：

**高优先级（用户报告的问题）：**
- `login.ts` - `changePassword` 和 `passwordChangeSuccess` 真正缺失
- `chat.ts` - 所有扁平键需要从嵌套结构移动

**其他文件：**
- `providerDetail.ts` - 移除冗余根级别 TODO
- `models.ts` - 移动嵌套翻译到根级别
- `keys.ts` - 移动嵌套翻译到根级别
- `requests.ts` - 移动嵌套翻译到根级别
- `pricingManagement.ts` - 移动嵌套翻译到根级别
- `standardModelPricing.ts` - 移动嵌套翻译到根级别
- `agentRegistryView.ts` - 移动嵌套翻译到根级别
- `freePool.ts` - 移动嵌套翻译到根级别
- `auditLog.ts` - 移动嵌套翻译到根级别
- `workTypes.ts` - 移动嵌套翻译到根级别
- `decisions.ts` - 移动嵌套翻译到根级别
- `compression.ts` - 移动嵌套翻译到根级别
- `dataLifecycle.ts` - 移动嵌套翻译到根级别
- `tenants.ts` - 移动嵌套翻译到根级别

#### 步骤 2：编写自动化修复脚本

创建 `web/scripts/i18n-cleanup.mjs` 脚本，功能：

1. 扫描所有语言文件中的 `[TODO: ...]` 标记
2. 对于每个 TODO 标记，检查：
   - 是否在嵌套结构中有对应翻译
   - Vue 组件是否使用扁平路径
3. 根据情况执行：
   - 移动嵌套翻译到根级别
   - 移除冗余 TODO 标记
   - 添加真正缺失的翻译（从源语言复制）

#### 步骤 3：手动修复真正缺失的翻译

对于 `login.ts` 中的 `changePassword` 和 `passwordChangeSuccess`：

**zh-CN:**
```typescript
changePassword: '修改密码',
passwordChangeSuccess: '密码修改成功',
```

**en-US:**
```typescript
changePassword: 'Change password',
passwordChangeSuccess: 'Password changed successfully',
```

其他语言需要翻译：

**ja-JP:**
```typescript
changePassword: 'パスワード変更',
passwordChangeSuccess: 'パスワードが正常に変更されました',
```

**de-DE:**
```typescript
changePassword: 'Passwort ändern',
passwordChangeSuccess: 'Passwort erfolgreich geändert',
```

**fr-FR:**
```typescript
changePassword: 'Changer le mot de passe',
passwordChangeSuccess: 'Mot de passe changé avec succès',
```

**es-ES:**
```typescript
changePassword: 'Cambiar contraseña',
passwordChangeSuccess: 'Contraseña cambiada exitosamente',
```

**ar-SA:**
```typescript
changePassword: 'تغيير كلمة المرور',
passwordChangeSuccess: 'تم تغيير كلمة المرور بنجاح',
```

**zh-TW:**
```typescript
changePassword: '修改密碼',
passwordChangeSuccess: '密碼修改成功',
```

#### 步骤 4：运行自动化脚本

执行清理脚本，处理所有其他 TODO 标记。

#### 步骤 5：验证修复结果

1. 运行 `npm run i18n:check` 验证无缺失键
2. 运行 `npm run i18n:check:strict` 进行严格检查
3. 运行 `npm test` 确保无回归
4. 手动检查关键页面的翻译显示

#### 步骤 6：提交代码

```bash
git add -A
git commit -m "fix(i18n): 清理所有 TODO 标记，修复多国语言处理不完整问题

- 修复 login.ts 中缺失的 changePassword 和 passwordChangeSuccess 翻译
- 移动未使用的嵌套翻译到根级别（如 chat.ts, models.ts 等）
- 移除冗余的根级别 TODO 标记（如 providerDetail.ts）
- 修复所有 8 个语言文件的翻译问题
- 解决用户报告的首页显示 [TODO: login.changePassword] 问题

Closes #TODO-i18n"
```

#### 步骤 7：合并到主分支

```bash
git checkout main
git merge fix-i18n-todo-markers
git push origin main
```

## 风险评估

### 低风险
- 移动翻译不会改变功能
- 只修改语言文件，不影响业务逻辑

### 中风险
- 可能遗漏某些 TODO 标记
- 翻译准确性需要人工验证

### 缓解措施
- 编写自动化脚本确保完整性
- 运行现有测试套件
- 人工审查关键翻译

## 预期结果

修复后：
1. 用户不再看到 `[TODO: ...]` 文本
2. 所有语言的翻译完整且正确
3. 现有功能不受影响
4. 代码质量提升，技术债务减少

## 时间估算

- 编写脚本：1-2 小时
- 手动修复缺失翻译：30 分钟
- 运行脚本和验证：30 分钟
- 测试和提交：30 分钟
- **总计：约 2.5-3 小时**