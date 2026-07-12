# /api/routing/resolve 500 错误修复：功能总结与流程图

## 功能总结

### 问题背景
`/api/routing/resolve` 接口是 LLM Gateway 管理后台的**路由诊断工具**，前端路由全景页调用 `?model=glm-5.2&persist_probe=1` 查询模型路由候选列表并记录探测日志。

### 故障现象
接口返回 HTTP 500，前端路由页面模型搜索功能不可用。

### 根因
视图 `v_routable_credential_models` 经 migration 327/332 重写后，不再暴露 `credential_status`/`availability_state`/`quota_state`/`binding_available`/`billing_mode` 等 8 个列，但 Go 代码 SQL 查询仍用 `v.xxx` 引用这些不存在的列 → PostgreSQL ERROR 42703。

### 修复与审计结果
1. **修复 SQL**: 将列引用从 `v.*` 改为已 JOIN 的 `credentials c.*` / `credential_model_bindings cmb.*`
2. **增强容错**: `persistResolveProbe` 添加 panic 恢复 + 详细错误日志
3. **审计更新**: 发现并修复 2 个过时视图定义文件（deploy/objects + installer/embeddata）
4. **部署**: 编译部署到 154 服务器 + 推送到 git main 分支
5. **验证**: browser-use 本地登录验证，路由页面正常显示

## 流程图

```mermaid
flowchart TD
    subgraph "👤 用户操作"
        A[前端路由全景页] -->|点击诊断| B[GET /api/routing/resolve<br/>?model=glm-5.2&persist_probe=1]
    end

    subgraph "🖥️ 服务端处理 (admin/routing.go)"
        B --> C[handleRoutingResolve]
        C --> D[查询 v_routable_credential_models]
        D --> E{视图查询是否成功?}
        E -->|✅ 是| F[读取凭证候选列表]
        E -->|❌ 否 - 列不存在 42703| G[返回 HTTP 500<br/>query failed]
        F --> H[计算 scoring weights]
        H --> I[遍历 candidates<br/>计算 CompositeScore]
        I --> J{persist_probe=1?}
        J -->|是| K[调用 persistResolveProbe]
        J -->|否| L[返回 200 + JSON]
        K --> M[INSERT INTO<br/>routing_decision_log_hot]
        M --> N{写入是否成功?}
        N -->|✅ 是| O[清理 funnel 缓存]
        N -->|❌ 否 - 增强容错| O
        O --> L
    end

    subgraph "🔍 视图定义 (v_routable_credential_models)"
        subgraph "❌ 过时定义 (原 deploy/objects)"
            V1[无 billing_mode<br/>无 plan_type<br/>无 plan_type_origin<br/>is_routable 逻辑不完整]
        end
        subgraph "✅ 正确定义 (migration 327/332)"
            V2[含 billing_mode<br/>含 plan_type<br/>含 plan_type_origin<br/>plan_type 兼容性检查<br/>is_routable 完整逻辑]
        end
        V1 -.->|审计发现并更新| V2
    end

    subgraph "📂 审计发现的问题"
        D1[deploy/sql/objects/views<br/>v_routable_credential_models.sql<br/>缺少 3 列 + 不完整逻辑] 
        D2[installer/cmd/llm-gw-installer<br/>embeddata/01-schema.sql<br/>缺少 3 列 + 不完整逻辑]
        D1 -->|已同步更新| V2
        D2 -->|已同步更新| V2
    end

    subgraph "🔬 测试验证"
        T1[browser-use 自动化测试]
        T2[打开 llm.kxpms.cn]
        T3[登录 admin/Veritrans&9527]
        T4[导航到路由全景页]
        T5[验证模型数据正常加载]
        T1 --> T2 --> T3 --> T4 --> T5
    end

    style G fill:#f96,stroke:#333
    style V2 fill:#9f6,stroke:#333
    style D1 fill:#ff9,stroke:#333
    style D2 fill:#ff9,stroke:#333
    style E fill:#bbf,stroke:#333
```

## 代码变更

| 文件 | 变更类型 | 说明 |
|---|---|---|
| `admin/routing.go` | 🛠️ 修复 | `v.*` → `c.*`/`cmb.*` 修正8个列引用 |
| `admin/routing_resolve_probe.go` | 🛡️ 增强 | panic 恢复 + 详细错误日志 |
| `deploy/sql/objects/views/v_routable_credential_models.sql` | 📝 审计修复 | 同步 migration 332 最新定义 |
| `installer/cmd/llm-gw-installer/embeddata/01-schema.sql` | 📝 审计修复 | 同步 migration 332 最新定义 |
| `docs/2026-07-09-routing-resolve-500-fix.md` | 📄 新增 | 修复记录文档 |

## Git 提交记录

```
c462cc45 docs(fix): 添加路由解析500错误修复记录 + 审计修正过时视图定义
17129047 fix(routing/resolve): 修复 v_routable_credential_models 视图字段不匹配导致500错误
```

## 部署确认

- **服务**: llm-gateway-go (154 服务器)
- **二进制**: `/opt/llm-gateway-go/llm-gateway-go`
- **状态**: active (running)
- **备份**: `/opt/llm-gateway-go/llm-gateway-go.bak.20260709_1157XX`
