# 附录 A — deploy.yml 完整 Schema

> llm-gateway-go v1.x 部署配置文件完整说明。

## 一、文件位置与版本

```yaml
# 版本声明
schema_version: 1      # 配置文件 schema 版本（务必匹配安装器）
```

## 二、字段详解

### 2.1 顶层字段

| 字段 | 必填 | 类型 | 默认 | 说明 |
|------|------|------|------|------|
| `schema_version` | ✅ | int | 1 | 配置 schema 版本 |
| `edition` | ✅ | string | customer | master / customer |
| `deployment_mode` | ✅ | string | standalone-offline | 见下方 |

#### edition 取值

- **master**：主控端，仅内部 CI 使用，不对外发布
- **customer**：客户端，对外发布（用户安装）

#### deployment_mode 取值

- **standalone-offline**：单机 + 离线包（客户专网）
- **standalone-online**：单机 + 公网拉镜像
- **k8s**：K8s/k3s 集群
- **docker**：Docker 直主机部署
- **db-only**：只部署 DB 节点

### 2.2 license 段

```yaml
license:
  server: "https://llm.kxpms.cn/api/v1"   # License Authority 地址
  activation_mode: "online"  # online | offline | cached
  
  trial:
    enabled: true
    duration_days: 15
  
  enforcement:
    check_clock_rollback: true
    check_fingerprint_score: 0.6
    max_clock_skew_sec: 300
    anti_debug: true
    anti_tamper: true
```

### 2.3 version 段

```yaml
version:
  package: "v1.13.0"
  git_tag: "r1.13.0"
  git_sha: "4f05275c"
  build_date: "20260712"
  build_seq: 770
  image_tag: "v1.13.0-770"
```

### 2.4 components 段

```yaml
components:
  app:
    enabled: true
    replicas: 3
    image: "kx-llm-gateway-go"
    tag: "v1.13.0-770"
    port: 8781
    resources:
      cpu: "1000m"
      memory: "2Gi"
      limits_cpu: "2000m"
      limits_memory: "4Gi"
  
  postgres:
    enabled: true
    image: "kx-citus:v11.3.0"
    port: 5432
    db_name: "llm_gateway"
    db_user: "kxuser"
    init_strategy: "dump-restore"  # fresh | dump-restore | migrate-only
    dump_file: "./db/dumps/llm_gateway_baseline.dump"
    pgdata_size_gb: 50
  
  redis:
    enabled: true
    image: "kx-redis:v7-alpine"
    port: 6379
    maxmemory: "1gb"
```

### 2.5 image_source 段

```yaml
image_source:
  strategy: "auto"  # auto | offline-only | registry-only
  offline_tar_dir: "./images"
  internal_registry: "registry.kxpms.cn"
  aliyun_mirror: "registry.cn-hangzhou.aliyuncs.com"
  require_signature: true
```

### 2.6 network 段

```yaml
network:
  bind_address: "0.0.0.0"
  expose_via:
    - port: 8781
      protocol: http
    - port: 8443
      protocol: https
```

### 2.7 persistence 段

```yaml
persistence:
  strategy: "bind-mount"  # bind-mount | pvc | nfs
  data_root: "/var/lib/kx-gateway"
  storage_class: "standard"  # K8s 专用
  access_mode: "ReadWriteOnce"
```

### 2.8 telemetry 段（新增）

```yaml
telemetry:
  enabled: false  # 默认关闭，用户首次激活时询问
  endpoint: "https://llm.kxpms.cn/api/v1/collect/runtime"
  interval_sec: 300
  include_model_usage: true
  include_resource_usage: true
  include_db_metrics: true
  include_tenant_count: true
```

### 2.9 upgrade 段（新增）

```yaml
upgrade:
  enabled: true
  auto_check: true
  auto_apply: false
  server: "https://llm.kxpms.cn/api/v1/updates"
  channel: "stable"
  blue_green_enabled: true
  grace_period_min: 30
```

### 2.10 anti_piracy 段（新增）

```yaml
anti_piracy:
  watermark_instance_id: true
  crash_on_tamper: true
  bind_to_fingerprint: true
  max_clock_skew_sec: 300
```

### 2.11 audit 段

```yaml
audit:
  log_dir: "/var/log/kx-gateway/audit"
  retention_days: 90
  remote_endpoint: ""
```

## 三、完整示例

### 3.1 master 版（内部）

```yaml
schema_version: 1
edition: master
deployment_mode: k8s

license:
  server: ""
  activation_mode: "online"
  enforcement:
    anti_debug: false
    anti_tamper: false

version:
  image_tag: "v1.14.0-master-20260720"
  build_seq: 800

components:
  app:    { enabled: true, replicas: 3 }
  postgres: { enabled: true }
  redis:  { enabled: true }

image_source:
  strategy: "auto"
  internal_registry: "registry.kxpms.cn"
```

### 3.2 customer 版（用户）

```yaml
schema_version: 1
edition: customer
deployment_mode: standalone-offline

license:
  key: "KXGW-XXXX-XXXX-XXXX"
  activation_mode: "online"
  trial: { enabled: true, duration_days: 15 }

version:
  package: "v1.13.0"
  image_tag: "v1.13.0-770"

components:
  app:    { enabled: true, port: 8781 }
  postgres: { enabled: true, init_strategy: "dump-restore" }
  redis:  { enabled: true }

image_source:
  strategy: "offline-only"

telemetry:
  enabled: false

upgrade:
  auto_apply: false
```

## 四、向后兼容

- 字段新增必须向后兼容（默认值合理）
- 字段删除需在 CHANGELOG 中标注 DEPRECATED
- 字段重命名需保留旧字段至少 6 个月