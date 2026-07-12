# R1.12 UI 验收流程图

## 完整登录与模块浏览流程

```mermaid
graph TD
    Start([用户访问 localhost:8781]) --> Landing[Landing Page<br/>显示 Sign in 按钮]
    Landing --> ClickSignIn[点击 Sign in]
    ClickSignIn --> LoginModal[登录弹窗打开<br/>显示用户名/密码输入框]
    
    LoginModal --> FillForm[填写表单<br/>admin / Veritrans&9527]
    FillForm --> SubmitLogin[点击登录按钮]
    
    SubmitLogin --> AuthAPI[POST /api/auth/token]
    AuthAPI --> CheckPassword{密码哈希验证}
    
    CheckPassword -->|$2b$ Python bcrypt| InvalidCreds[401 Invalid credentials]
    CheckPassword -->|$2a$ Go bcrypt| AuthSuccess[200 OK<br/>返回 access_token + user]
    
    AuthSuccess --> SaveToken[setJwtToken<br/>localStorage: llmgw_api_key]
    SaveToken --> SaveUser[setUserInfo<br/>localStorage: llmgw_user_info]
    
    SaveUser --> CloseModal[关闭登录弹窗]
    CloseModal --> CheckRedirect{是否有 redirect 参数?}
    
    CheckRedirect -->|有| RedirectTarget[router.replace<br/>跳转到目标页面]
    CheckRedirect -->|无| StayHome[留在首页]
    
    RedirectTarget --> ModulesPage
    StayHome --> UserClickModules[用户手动导航到<br/>/admin/modules]
    UserClickModules --> ModulesPage
    
    ModulesPage[Modules 页面加载] --> FetchModules[GET /api/admin/modules<br/>带 Authorization header]
    FetchModules --> CheckAuth{JWT token 有效?}
    
    CheckAuth -->|无效/过期| Redirect401[重定向到 /?login=1]
    CheckAuth -->|有效| RenderModules[渲染模块列表<br/>显示 13/17 modules enabled]
    
    RenderModules --> ShowWechat[显示微信机器人卡片<br/>状态: Disabled / Safe level]
    ShowWechat --> ClickWechat[用户点击微信机器人]
    
    ClickWechat --> WechatDetail[模块详情页<br/>显示 Description, Capabilities, Config]
    WechatDetail --> ShowPrereqs[显示依赖模块<br/>压缩管理/提示词注入检测/会话缓存]
    
    ShowPrereqs --> End([验收完成])
    
    InvalidCreds --> RetryLogin[用户重试登录]
    RetryLogin --> FixPassword[管理员修复密码哈希<br/>使用 Go bcrypt]
    FixPassword --> FillForm
    
    Redirect401 --> ClickSignIn

    style AuthSuccess fill:#90EE90
    style InvalidCreds fill:#FFB6C6
    style SaveUser fill:#87CEEB
    style RenderModules fill:#90EE90
    style WechatDetail fill:#90EE90
```

## 密码验证流程（关键路径）

```mermaid
sequenceDiagram
    participant B as Browser
    participant G as Gateway
    participant PG as PostgreSQL
    
    B->>G: POST /api/auth/token<br/>{username, password}
    G->>PG: SELECT password_hash FROM users<br/>WHERE username='admin'
    PG-->>G: $2a$10$... (Go bcrypt)
    
    Note over G: bcrypt.CompareHashAndPassword<br/>(hash, password)
    
    alt Password hash prefix $2a$ (Go bcrypt)
        G-->>B: 200 OK<br/>{access_token, user}
        B->>B: localStorage.setItem<br/>('llmgw_user_info', user)
        B->>B: localStorage.setItem<br/>('llmgw_api_key', token)
    else Password hash prefix $2b$ (Python bcrypt)
        G-->>B: 401 Unauthorized<br/>{error: "Invalid credentials"}
        Note over B: 登录失败，用户看到错误提示
    end
```

## 数据库初始化流程

```mermaid
graph LR
    A[docker-compose up] --> B[PostgreSQL 容器启动]
    B --> C{数据库已存在?}
    
    C -->|否| D[创建空数据库 llm_gateway]
    C -->|是| E[跳过初始化]
    
    D --> F[应用 01-schema.sql<br/>创建所有表/索引/触发器]
    F --> G[应用 02-seed.sql<br/>插入 seed 数据]
    G --> H[Gateway 启动]
    
    E --> H
    
    H --> I[db.Open 连接池]
    I --> J[ensureRequestLogSchema<br/>ALTER TABLE 添加列]
    
    J --> K{request_logs 表存在?}
    K -->|否| L[ERROR: relation does not exist<br/>postgres disabled]
    K -->|是| M[ensureSeedAdmin<br/>创建/更新 admin 用户]
    
    M --> N{admin 用户存在?}
    N -->|否| O[INSERT admin 用户<br/>密码哈希来自 SEED_ADMIN_PASSWORD]
    N -->|是| P[跳过 seed]
    
    O --> Q{哈希格式正确?}
    Q -->|$2a$ Go| R[登录可用]
    Q -->|$2b$ Python| S[登录失败 401]
    
    P --> Q
    
    L --> T[Gateway 无数据库功能<br/>但仍可提供静态文件]
    
    style R fill:#90EE90
    style S fill:#FFB6C6
    style L fill:#FFB6C6
```

## browser-use 测试覆盖

| 测试项 | 验证内容 | 截图 |
|--------|---------|------|
| 1. Landing page loads | Sign in 按钮可见 | `/tmp/ui-verify-landing-desktop-01.png` |
| 2. Login modal opens | 用户名/密码输入框存在 | `/tmp/ui-verify-login-modal-02.png` |
| 3. Login succeeds | POST /api/auth/token 返回 200 | - |
| 4. User info saved | localStorage 包含 `llmgw_user_info` | `/tmp/ui-verify-after-login-03.png` |
| 5. User is admin | `username === 'admin'` | - |
| 6. User is super_admin | `role === 'super_admin'` | - |
| 7. Modules page loads | URL 包含 `/admin/modules` | `/tmp/ui-verify-modules-desktop-04.png` |
| 8. WeChat module visible | 页面包含 "微信" 文本 | - |
| 9. WeChat detail loads | 详情包含 Description/Capabilities | `/tmp/ui-verify-wechat-detail-05.png` |
| 10. Mobile layout | 375x667 视口下 Sign in 可见 | `/tmp/ui-verify-landing-mobile-06.png` |
| 11. Dark theme default | `body.backgroundColor !== white` | - |

**通过率**: 10/11 (90%)

## 常见失败场景与根因

### 场景 1: 登录后 403 "password change required"

```mermaid
graph LR
    A[用户登录成功] --> B[GET /api/admin/modules]
    B --> C{must_change_password = true?}
    C -->|是| D[403 Forbidden<br/>middleware 拦截]
    C -->|否| E[200 OK 返回模块列表]
    
    D --> F[前端显示密码修改提示]
    
    style D fill:#FFB6C6
    style E fill:#90EE90
```

**修复**:
```sql
UPDATE users SET must_change_password = false WHERE username = 'admin';
```

### 场景 2: 密码哈希不匹配

```mermaid
graph TD
    A[Python 生成 $2b$ 哈希] --> B[写入数据库]
    B --> C[Go bcrypt 读取哈希]
    C --> D{识别 $2b$ 前缀?}
    D -->|否| E[CompareHashAndPassword 失败]
    E --> F[返回 401]
    
    G[Go 生成 $2a$ 哈希] --> H[写入数据库]
    H --> I[Go bcrypt 读取哈希]
    I --> J{识别 $2a$ 前缀?}
    J -->|是| K[CompareHashAndPassword 成功]
    K --> L[返回 200 + token]
    
    style E fill:#FFB6C6
    style F fill:#FFB6C6
    style K fill:#90EE90
    style L fill:#90EE90
```

## 相关文档

- `docs/r112-local-deployment-guide.md` - 部署指南
- `web/src/components/LoginModal.vue` - 登录组件
- `web/src/api/auth.ts` - 认证 API
- `web/src/store.ts` - 状态管理（localStorage）
