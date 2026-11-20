# 211 用户认证系统配置报告

## 执行日期
2026-11-20

## 任务概述
检查并配置 211 网关的用户认证系统，修改 admin 密码，创建测试用户账号。

## 1. 系统架构确认

### 认证系统类型
- **结论**：211 使用 **原生认证系统**（Native Authentication）
- **技术栈**：PostgreSQL + bcrypt 密码哈希
- **NOT Casdoor**：系统未配置 Casdoor 集成

### 数据库配置
- **数据库**：acc-pg17 容器（PostgreSQL 17）
- **数据库名**：llm_gateway
- **用户**：acc_super
- **连接**：127.0.0.1:5432

### 用户表结构
```sql
CREATE TABLE public.users (
    id integer NOT NULL,
    tenant_id character varying(64) DEFAULT 'default'::character varying NOT NULL,
    username character varying(128) NOT NULL,
    password_hash character varying(256) NOT NULL,
    display_name character varying(128) DEFAULT ''::character varying NOT NULL,
    email character varying(256) DEFAULT ''::character varying NOT NULL,
    role character varying(32) DEFAULT 'tenant_admin'::character varying NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    last_login_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    must_change_password boolean DEFAULT false NOT NULL
);
```

## 2. 用户配置详情

### 2.1 admin 用户（系统管理员）
- **用户名**：admin
- **密码**：Veritrans&9527
- **角色**：super_admin
- **显示名称**：系统管理员
- **租户**：default
- **ID**：41
- **状态**：enabled = true
- **密码哈希**：$2a$10$0S2anEDieoqe5zscG4LRAumOc4x4Z8LH6MUwC8M.tjTv0m4BIkFKq (60字符)
- **更新时间**：2026-11-20 23:40:27.433893+08
- **must_change_password**：false

### 2.2 demoadmin 用户（演示管理员）
- **用户名**：demoadmin
- **密码**：Demo@admin123
- **角色**：super_admin
- **显示名称**：演示管理员
- **租户**：default
- **ID**：47
- **状态**：enabled = true
- **密码哈希**：$2a$10$l6zK70rODKa23Xt1SwVuleoDvQpr3OZjcy1kqO3XToI4FzHFNoXhO (60字符)
- **must_change_password**：false

### 2.3 demo 用户（演示普通用户）
- **用户名**：demo
- **密码**：Demo@user123
- **角色**：tenant_admin
- **显示名称**：演示用户
- **租户**：default
- **ID**：48
- **状态**：enabled = true
- **密码哈希**：$2a$10$3Czeg2wqBAIlflyQrMRUGuw0aBpbALsB9mvOL2GFGuTiHh8imngXC (60字符)
- **must_change_password**：false

## 3. 密码哈希技术规格

### 哈希算法
- **算法**：bcrypt
- **Cost Factor**：10（默认）
- **哈希长度**：60字符
- **格式**：$2a$10$[53字符盐+哈希]

### 验证方式
所有密码哈希均使用 `golang.org/x/crypto/bcrypt` 生成，与系统中现有用户的哈希格式完全一致。

## 4. 数据库验证

### SQL 验证查询
```sql
SELECT id, username, display_name, role, enabled, 
       length(password_hash) as hash_len,
       substring(password_hash, 1, 15) as hash_prefix
FROM users 
WHERE username IN ('admin', 'demoadmin', 'demo')
ORDER BY id;
```

### 验证结果
```
 id | username  | display_name |     role     | enabled | hash_len |    hash_prefix    
----+-----------+--------------+--------------+---------+----------+-------------------
 41 | admin     | 系统管理员   | super_admin  | t       |       60 | $2a$10$0S2anEDi
 47 | demoadmin | 演示管理员   | super_admin  | t       |       60 | $2a$10$l6zK70rO
 48 | demo      | 演示用户     | tenant_admin | t       |       60 | $2a$10$3Czeg2wq
```

✓ 所有三个用户的密码哈希格式正确
✓ 哈希长度均为 60 字符（标准 bcrypt 格式）
✓ 所有用户状态为 enabled
✓ 密码不需要强制更改

## 5. 登录端点

### API 端点
- **URL**：https://llm.itestu.cn/api/auth/token
- **方法**：POST
- **Content-Type**：application/json

### 请求格式
```json
{
  "username": "admin",
  "password": "Veritrans&9527"
}
```

### 成功响应
```json
{
  "access_token": "eyJ...",
  "token_type": "Bearer",
  "expires_at": "2026-11-21T...",
  "user": {
    "id": 41,
    "username": "admin",
    "display_name": "系统管理员",
    "role": "super_admin",
    ...
  }
}
```

## 6. 安全特性

### 验证码保护
- **触发条件**：连续登录失败后自动触发
- **端点**：GET /api/auth/captcha
- **状态**：当前因前期测试失败而激活
- **冷却时间**：等待自动过期或清除 Redis 缓存

### 密码策略
- 使用 bcrypt 算法，计算成本因子为 10
- 密码哈希存储在数据库中
- admin 用户有特殊的环境变量回退机制（LLM_GATEWAY_ADMIN_PASSWORD）

## 7. 手动验证步骤

### 7.1 浏览器验证（推荐）
1. 访问：https://llm.itestu.cn/?login=1
2. 使用以下凭据登录：
   - **admin** / Veritrans&9527
   - **demoadmin** / Demo@admin123
   - **demo** / Demo@user123
3. 验证登录成功并查看用户菜单中的显示名称

### 7.2 API 验证（需等待验证码冷却）
```bash
# 测试 admin 登录
curl -X POST https://llm.itestu.cn/api/auth/token \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "Veritrans&9527"}'

# 测试 demoadmin 登录
curl -X POST https://llm.itestu.cn/api/auth/token \
  -H "Content-Type: application/json" \
  -d '{"username": "demoadmin", "password": "Demo@admin123"}'

# 测试 demo 登录
curl -X POST https://llm.itestu.cn/api/auth/token \
  -H "Content-Type: application/json" \
  -d '{"username": "demo", "password": "[REDACTED:secret]"}'
```

## 8. Maestro 自动化测试

### 测试文件
- **位置**：`maestro/flows/13-user-auth-verification.yaml`
- **测试内容**：
  1. admin 用户登录验证
  2. demoadmin 用户登录验证（super_admin 权限）
  3. demo 用户登录验证（tenant_admin 权限）

### 运行命令
```bash
cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go
maestro test maestro/flows/13-user-auth-verification.yaml \
  -e BASE_URL=https://llm.itestu.cn
```

### 注意事项
- Maestro 测试需要在验证码冷却后执行
- 或者在首次运行前清除 Redis 中的登录失败计数

## 9. 问题与限制

### 当前问题
1. **验证码触发**：由于前期密码测试失败，验证码保护已激活
2. **冷却时间**：需要等待一段时间让失败计数自动过期

### 解决方案
1. **等待验证码过期**：通常几分钟到几小时
2. **清除 Redis 缓存**：`docker exec [redis-container] redis-cli -n 2 DEL login:failures:*`
3. **使用浏览器测试**：浏览器可以输入验证码完成登录

## 10. 总结

### 完成项目
✅ 确认 211 使用原生认证系统（非 Casdoor）
✅ 检查数据库 users 表结构和现有用户（24个用户）
✅ 修改 admin 用户密码为 Veritrans&9527
✅ 创建 demoadmin 用户（super_admin 权限）
✅ 创建 demo 用户（tenant_admin 权限）
✅ 验证所有密码哈希格式正确（60字符 bcrypt）
✅ 创建 Maestro 自动化测试脚本

### 待验证项目
⏳ Maestro 自动化测试执行（需验证码冷却）
⏳ 浏览器手动登录验证（推荐作为最终验证）

### 下一步建议
1. 等待 10-30 分钟让验证码冷却
2. 使用浏览器手动验证三个用户登录
3. 执行 Maestro 自动化测试
4. 确认所有用户可以正常访问相应权限的功能

## 附录：技术工具

### 密码哈希生成工具
创建位置：`cmd/hash-password/main.go`
```bash
cd cmd/hash-password
go run main.go "your-password-here"
```

### 用户管理工具
创建位置：`cmd/user-admin-tool/main.go`
功能：列出用户、修改密码、创建用户
