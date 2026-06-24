# SDP 集成可行性调研（Presidio + 中文 PII 增强）

> **文档版本**：v1.0
> **调研日期**：2026-06-25
> **作者**：kaixuan-ai-agent
> **配套**：实施计划 `2026-06-23-llmgw-implementation-plan.md` Part 4 B2（Q4 第 6-9 周）
> **依赖**：`security/armor/` 包（NOW-2 已完成，commit `ecea00cc`）
>
> **TL;DR（选型结论）**：采用 **Presidio Python sidecar（HTTP REST）+ 自研中文 PII 规则包**。
> 不用 Go port（均为非官方、低活跃、2 星以下）。sidecar 模式让我们获得 Presidio 官方全部能力，
> 同时 Go 主进程零 Python 依赖，部署可接受（184 k3s 额外 +1 容器，~150MB 内存）。

---

## 1. 选型结论

| 方案 | 优点 | 缺点 | 评分 |
|------|------|------|------|
| **A. Presidio Python sidecar（HTTP）** ✅ 推荐 | 官方全功能；社区维护；中文可插件；Go 零依赖 | +1 容器；网络 RTT ~2ms | **9/10** |
| B. Presidio Python sidecar（gRPC） | 比 HTTP 快 ~30% | 需生成 protobuf；调试复杂 | 7/10 |
| C. Go port（karldane/go-presidio 等） | 单体二进制；无 sidecar | 非官方；2 star；功能残缺；无中文支持 | 2/10 |
| D. 纯 Go 自研 | 单体；可控 | 重复造轮；NLP 能力弱；3+ 月工作量 | 4/10 |
| E. 云 API（阿里云/百度 PII） | 即用；中文强 | 数据出境合规风险；延迟不可控；按量计费 | 3/10 |

**决策：方案 A**。

理由：
1. **Presidio 是行业事实标准**（microsoft/presidio，9.5k+ stars，MIT 协议，Microsoft 官方维护）。
2. **Go 生态无成熟替代**：GitHub 搜索 `presidio golang`，最高星仓库 `CodeRunRepeat/presidio-go-client` 仅 2 星，且是 REST 客户端封装（仍需 Presidio 服务端）。`karldane/go-presidio` 是部分重写，1 星，无中文支持。
3. **sidecar 模式符合现有架构**：184 k3s 已有 Neo4j / Qdrant / PG 等多容器模式，多一个 Presidio 容器无运维负担。
4. **中文 PII 必须自研增强**：Presidio 原生中文支持有限（依赖 `zh_core_web` spaCy 模型，身份证/银行卡需自定义），无论选哪个方案都要写中文规则——sidecar 方案下规则写在 Python 侧，迭代最快。

---

## 2. Presidio 集成方案

### 2.1 架构

```
┌──────────────────────────────────────────────────────────────┐
│  184 k3s pod: kx-llm-gateway-go                               │
│  ┌────────────────────────────────────────────────────────┐  │
│  │  Go 主进程 (relay handler)                              │  │
│  │    │                                                    │  │
│  │    ▼                                                    │  │
│  │  armor/sdp.go  ──── HTTP POST ──────┐                   │  │
│  │    Sanitize(ctx, text, policy)       │ (JSON, ~2ms RTT)  │  │
│  │    返回 sanitized text + spans       │                   │  │
│  │                                      ▼                   │  │
│  └──────────────────────────────────────┐                  │  │
│                                         │                  │  │
│  ┌──────────────────────────────────────┘                  │  │
│  │ 184 k3s pod: presidio-sidecar (Deployment)               │  │
│  │  ┌──────────────────────────────────────────────────┐   │  │
│  │  │  Python 3.11 + presidio-analyzer + anonymizer     │   │  │
│  │  │  + 自研 zh_pii_rules 包（身份证/银行卡/车牌/地址）  │   │  │
│  │  │  Flask/FastAPI HTTP server (port 3004)             │   │  │
│  │  │  /analyze   → 返回 PII spans                        │   │  │
│  │  │  /anonymize → 返回脱敏后文本                        │   │  │
│  │  └──────────────────────────────────────────────────┘   │  │
│  └────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────┘
```

**Go 侧接口设计**（`armor/sdp.go`，Q4 B2-1 实现）：

```go
// Package armor 的 SDP（Sensitive Data Protection）子模块。
// 通过 HTTP sidecar 调用 Presidio，支持 mask / hash / block 三种模式。

package armor

// SDPClient 是 Presidio sidecar 的 Go 客户端。
type SDPClient struct {
    endpoint   string        // e.g. "http://presidio-sidecar.pms-test:3004"
    httpClient *http.Client
    timeout    time.Duration // 默认 3s，避免阻塞 relay 热路径
}

// SDPMode 控制命中 PII 后的处理方式。
type SDPMode int
const (
    SDPModeMask  SDPMode = iota // 替换为 [PHONE]/[EMAIL] 占位符
    SDPModeHash                  // 替换为 SHA256 前 8 位
    SDPModeBlock                 // 整个请求拒绝（返回 error）
)

// SDPPolicy 是 per-tenant 的脱敏策略。
type SDPPolicy struct {
    TenantID    string
    EnabledTypes []string  // ["email","phone","id_card_cn","bank_card_cn","plate_cn","address_cn"]
    Mode        SDPMode
    // SidecarUnavailablePolicy: sidecar 挂了怎么办？
    //   "fail_open"  → 放行（仅告警，v1 默认）
    //   "fail_closed" → 拒绝（高安全租户可选）
    FailPolicy  string
}

// Sanitize 对入站 prompt 做脱敏，返回脱敏后文本 + 命中的 PII span 列表。
// 线程安全，可在 relay 热路径并发调用。
func (c *SDPClient) Sanitize(ctx context.Context, text string, p SDPPolicy) (sanitized string, hits []PIISpan, err error)
```

**关键设计点**：
- **同步调用，但有超时**：`Sanitize` 在 relay handler 链路里同步执行（prompt 必须脱敏后才能发给 LLM）。超时 3s，超时后按 `FailPolicy` 处理。
- **双向脱敏**：入站（用户 prompt）+ 出站（LLM response，含 SSE 流式）。出站脱敏见 §4。
- **批量优化**：sidecar 支持 batch `/anonymize`（一次多条文本），减少 RTT。

### 2.2 性能预估

基于 Presidio 官方 benchmark + sidecar RTT 经验值：

| 场景 | 文本长度 | 端到端 P50 | 端到端 P99 | 吞吐 (QPS) |
|------|---------|-----------|-----------|-----------|
| 短 prompt（< 500 字符） | 300 char | 8 ms | 25 ms | 1200 |
| 中 prompt（500-2000 字符） | 1 KB | 15 ms | 45 ms | 600 |
| 长 prompt（2-10 KB） | 5 KB | 40 ms | 120 ms | 200 |
| 超长 prompt（> 10 KB） | 20 KB | 120 ms | 400 ms | 80 |

**Presidio sidecar 资源占用**：
- CPU：空闲 50m，满载 1 core（可 limit 2 core）
- 内存：~150MB（spaCy `zh_core_web_sm` + 规则引擎）
- 启动时间：~8s（spaCy 模型加载）

**对 relay 热路径的影响**：
- 当前 relay P50 = 850ms（含 LLM 调用），SDP 增加 ~15ms → **+1.8%**，可接受。
- 若 sidecar 故障，`FailPolicy=fail_open` 时降级为"仅记录告警"，不阻断业务。

### 2.3 部署方案

**184 k3s 部署**（新增 Deployment + Service）：

```yaml
# k8s/apps/presidio-sidecar.yaml (Q4 B2-1 新增)
apiVersion: apps/v1
kind: Deployment
metadata:
  name: presidio-sidecar
  namespace: pms-test
spec:
  replicas: 2                    # HA：2 副本，LB
  selector:
    matchLabels: { app: presidio-sidecar }
  template:
    metadata:
      labels: { app: presidio-sidecar }
    annotations:
      # OTel 注入（遵循 lint-otel-tenant 规范）
      sidecar.opentelemetry.io/inject: "true"
    spec:
      containers:
      - name: presidio
        image: registry.internal.example.com/kx-presidio-sidecar:v0.1  # 自建镜像
        ports: [{ containerPort: 3004 }]
        resources:
          requests: { cpu: 200m, memory: 200Mi }
          limits:   { cpu: 2,    memory: 512Mi }
        readinessProbe:
          httpGet: { path: /healthz, port: 3004 }
          initialDelaySeconds: 10
        livenessProbe:
          httpGet: { path: /healthz, port: 3004 }
          periodSeconds: 30
        env:
        - name: SPACY_MODEL       # 默认 zh_core_web_sm
          value: "zh_core_web_sm"
        - name: ENABLE_CUSTOM_RULES
          value: "true"
---
apiVersion: v1
kind: Service
metadata:
  name: presidio-sidecar
  namespace: pms-test
spec:
  selector: { app: presidio-sidecar }
  ports:
  - port: 3004
    targetPort: 3004
```

**镜像构建**（`deploy/shared/docker/presidio-sidecar/Dockerfile`）：
- 基础：`docker.m.daocloud.io/library/python:3.11-slim`（遵循镜像仓库规则，禁止直连 docker.io）
- 安装：`presidio-analyzer` + `presidio-anonymizer` + `spacy` + `zh_core_web_sm`
- 自研包：`/app/zh_pii_rules/`（身份证/银行卡/车牌/地址正则 + NLP）

**71 部署**（llm-gateway-go 71 实例也需要 SDP）：
- 71 非 k3s，用 docker-compose 起 sidecar 容器
- Go 主进程 env `SDP_ENDPOINT=http://127.0.0.1:3004`

---

## 3. 中文 PII 增强

Presidio 原生支持英文 PII（SSN、email、phone、credit card 等），**中文 PII 需自研规则包**。

### 3.1 中文 PII 类型与检测方案

| PII 类型 | 检测方法 | 召回率目标 | 精确率目标 | 难度 |
|---------|---------|-----------|-----------|------|
| **手机号** | 正则 `1[3-9]\d{9}` | 99% | 99% | 易 |
| **身份证号** | 正则 + 校验码（GB 11643） | 98% | 99% | 中 |
| **银行卡号** | 正则 + Luhn 校验 | 95% | 98% | 中 |
| **车牌号** | 正则（省简称 + 字母数字） | 92% | 95% | 中 |
| **邮箱** | Presidio 内置 | 99% | 99% | 易 |
| **地址** | NER（spaCy `zh_core_web_sm`）+ 规则后处理 | 75% | 85% | 难 |
| **姓名** | NER（spaCy）+ 常见姓氏表 | 70% | 80% | 难 |

### 3.2 实现细节

**身份证号校验**（GB 11643-1999）：
```python
# zh_pii_rules/id_card.py
import re

ID_CARD_PATTERN = re.compile(r'\b[1-9]\d{5}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]\b')

def validate_id_card(id_str: str) -> bool:
    """GB 11643 校验码验证。"""
    if len(id_str) != 18:
        return False
    weights = [7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2]
    check_map = ['1','0','X','9','8','7','6','5','4','3','2']
    total = sum(int(id_str[i]) * weights[i] for i in range(17))
    return check_map[total % 11] == id_str[17].upper()
```

**银行卡号校验**（Luhn 算法）：
```python
# zh_pii_rules/bank_card.py
def validate_bank_card(card: str) -> bool:
    digits = [int(d) for d in card if d.isdigit()]
    if len(digits) < 13 or len(digits) > 19:
        return False
    # Luhn checksum
    total, parity = 0, len(digits) % 2
    for i, d in enumerate(digits):
        if i % 2 == parity:
            d *= 2
            if d > 9: d -= 9
        total += d
    return total % 10 == 0
```

**车牌号**（含新能源 6 位）：
```python
# zh_pii_rules/plate.py
import re
# 省：京津沪渝冀豫云辽黑湘皖鲁新苏浙赣鄂桂甘晋蒙陕吉闽贵粤青藏川宁琼
PLATE_PATTERN = re.compile(
    r'[京津沪渝冀豫云辽黑湘皖鲁新苏浙赣鄂桂甘晋蒙陕吉闽贵粤青藏川宁琼]'
    r'[A-Z][A-HJ-NP-Z0-9]{4,5}[A-HJ-NP-Z0-9]'  # 普通牌 + 新能源牌
)
```

### 3.3 Presidio 自定义 Recognizer 注册

```python
# zh_pii_rules/__init__.py —— 注册到 Presidio
from presidio_analyzer import AnalyzerEngine, RecognizerRegistry
from presidio_analyzer.predefined_recognizers import EmailRecognizer, PhoneRecognizer
from .id_card import IDCardRecognizer
from .bank_card import BankCardRecognizer
from .plate import PlateRecognizer

def build_registry() -> RecognizerRegistry:
    registry = RecognizerRegistry()
    registry.add_recognizer(EmailRecognizer())
    registry.add_recognizer(PhoneRecognizer())
    registry.add_recognizer(IDCardRecognizer())   # 自研
    registry.add_recognizer(BankCardRecognizer()) # 自研
    registry.add_recognizer(PlateRecognizer())    # 自研
    return registry
```

### 3.4 测试集（Q4 B2-5 交付）

构造 **100 条中文 PII 测试语料**（脱敏自真实场景，不含真实数据）：
- 30 条手机号（不同号段、带分隔符、国际格式）
- 20 条身份证（跨省份、跨年代、含 X 校验码）
- 15 条银行卡（主流银行 BIN 段）
- 15 条车牌（普通 + 新能源 + 军车）
- 20 条地址 + 姓名混合（NER 难例）

**验收**：召回率 ≥ 95%（身份证/手机号/银行卡），≥ 85%（车牌/地址）。

---

## 4. 流式脱敏（SSE）

LLM 响应通常是 SSE 流式输出，PII 可能**跨 chunk 边界**出现（如手机号 "138-1234-5678" 被切成 "138-123" + "4-5678"）。

### 4.1 问题示意

```
chunk 1: "您的订单号是 OD-123，联系电话 138-12"
chunk 2: "34-5678，收货地址是北京市朝阳区..."
                                    ↑
                         "138-1234-5678" 被切成两半，正则无法命中
```

### 4.2 状态缓冲方案

```go
// armor/sdp_stream.go (Q4 B2-4 实现)

// SDPStreamSanitizer 是一个有状态的流式脱敏器。
// 它维护一个滑动缓冲区，在检测到可能的 PII 前缀时暂不输出，
// 直到确认是 PII（mask）或不是（原样输出）。
type SDPStreamSanitizer struct {
    client   *SDPClient
    policy   SDPPolicy
    buf      strings.Builder  // 滑动缓冲
    maxBuffer int             // 默认 32 字节（覆盖最长 PII = 身份证 18 位 + 分隔符）
    flushed   int             // 已确认安全并 flush 的偏移
}

// OnChunk 处理一个 SSE chunk，返回可安全输出给客户端的字节。
// 内部逻辑：
//   1. 追加 chunk 到 buf
//   2. 扫描 buf[flushed:] 查找 PII
//   3. 对于"可能是 PII 前缀"的尾部（如 "138-12"），暂不 flush
//   4. 对于确认安全的区间，flush 给客户端
//   5. 对于确认的 PII，替换为 [PHONE] 占位符后 flush
func (s *SDPStreamSanitizer) OnChunk(chunk []byte) ([]byte, []PIISpan, error)

// Flush 在 SSE 流结束时调用，强制清空缓冲区。
func (s *SDPStreamSanitizer) Flush() ([]byte, []PIISpan, error)
```

**关键设计**：
- **`maxBuffer = 32`**：覆盖最长 PII（18 位身份证 + 分隔符 + 容错）。
- **延迟 flush**：尾部 32 字节始终留在缓冲区，直到下一个 chunk 来了或流结束。
- **性能影响**：内存每连接 +32 字节；CPU 增加一次缓冲区扫描，可忽略。

### 4.3 边界情况

| 情况 | 处理 |
|------|------|
| chunk 内含完整 PII | 正则命中 → mask |
| PII 跨 chunk | 缓冲区暂存，待下个 chunk 拼接 |
| 流结束时缓冲区有疑似 PII | 保守 mask（宁可误杀） |
| sidecar 超时 | 按 `FailPolicy`（fail_open 放行 / fail_closed 拒绝） |
| 客户端断连 | `ctx.Done()` → 清理缓冲区 |

---

## 5. 风险评估

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| **误判（false positive）** | 中 | 中：正常文本被 mask 影响用户体验 | (1) 校验码过滤（身份证/银行卡）；(2) per-tenant 白名单；(3) observe 模式先跑 2 周统计 FPR |
| **漏判（false negative）** | 中 | 高：PII 泄漏到 LLM | (1) 多规则组合（正则 + NLP）；(2) 定期更新攻击模式库；(3) 审计日志追溯 |
| **性能瓶颈** | 低 | 中：relay 延迟增加 | (1) sidecar 水平扩容；(2) 短文本 batch；(3) FailPolicy=fail_open 兜底 |
| **sidecar 故障** | 低 | 中：脱敏失效 | (1) 2 副本 HA；(2) 健康检查；(3) fail_open + 告警（fail_closed 仅对高安全租户） |
| **多语言扩展** | 低 | 低：未来支持日文/韩文 | Presidio 支持 spaCy 多语言模型，新增 recognizer 即可 |
| **合规审查** | 中 | 高：v1 enforce 模式需法务签字 | 严格遵循 armor v1 规范：observe-only，不拦截。enforce 需单独 PR + legal sign-off |
| **镜像构建（GFW）** | 中 | 中：spaCy 模型下载受阻 | 构建时用国内 PyPI mirror（`pip install -i https://pypi.tuna.tsinghua.edu.cn/simple`）；spaCy 模型预下载入镜像 |

---

## 6. 实施清单（Q4 B2，对应实施计划）

按依赖顺序：

| 步骤 | 任务 | 工作量 | 依赖 | 验收 |
|------|------|--------|------|------|
| **6.1** | 自建 Presidio sidecar 镜像（Python + zh_pii_rules） | 3 人日 | 无 | `docker run` 本地起，`curl /analyze` 返回 spans |
| **6.2** | k8s manifest（Deployment + Service）部署到 184 | 1 人日 | 6.1 | `kubectl get pod presidio-sidecar` Running |
| **6.3** | Go 侧 `armor/sdp.go`（SDPClient + Sanitize） | 2 人日 | 6.2 | `go test ./security/armor/...` ≥ 5 PASS |
| **6.4** | 中文 PII 规则包（身份证/银行卡/车牌） | 3 人日 | 6.1 | 100 条测试集召回 ≥ 95% |
| **6.5** | 地址/姓名 NER 增强（spaCy + 规则后处理） | 2 人日 | 6.4 | 100 条测试集召回 ≥ 85% |
| **6.6** | 流式脱敏 `armor/sdp_stream.go`（状态缓冲） | 3 人日 | 6.3 | SSE 跨 chunk PII 测试 PASS |
| **6.7** | 集成到 relay handler（双向脱敏） | 2 人日 | 6.3, 6.6 | 端到端：真实 prompt → 脱敏 → LLM → 脱敏 → 客户端 |
| **6.8** | per-tenant 策略表（DB migration）+ Admin API | 1 人日 | 6.3 | `GET /api/admin/sdp/policy/:tenant` |
| **6.9** | 前端 SDP 配置 UI（enable/types/mode） | 2 人日 | 6.8 | `/sdp` 页面可配置 |
| **6.10** | observe 模式上线 + 2 周 FPR/FNR 统计 | 1 人日 | 6.7 | 审计报告：FPR < 5%，FNR < 5% |

**总工作量**：~20 人日（4 人 × 1 周，或 2 人 × 2 周）。

**关键里程碑**：
- **M1（6.1-6.3 完成）**：sidecar + Go 客户端跑通，可对静态文本脱敏。
- **M2（6.4-6.5 完成）**：中文 PII 召回达标。
- **M3（6.6-6.7 完成）**：relay 集成，端到端流式脱敏。
- **M4（6.10 完成）**：observe 上线，数据驱动调优。

---

## 7. 参考资料

| 资料 | URL | 用途 |
|------|-----|------|
| Presidio 官方文档 | https://microsoft.github.io/presidio/ | 架构 / API / 自定义 recognizer |
| Presidio GitHub | https://github.com/microsoft/presidio | 源码（9.5k stars，MIT） |
| Presidio 自定义 Recognizer 教程 | https://microsoft.github.io/presidio/analyzer/adding-recognizers/ | 中文规则开发 |
| spaCy 中文模型 | https://spacy.io/models/zh | `zh_core_web_sm` NER |
| GB 11643-1999 | 居民身份证标准 | 校验码算法 |
| Luhn 算法 | https://en.wikipedia.org/wiki/Luhn_algorithm | 银行卡校验 |
| CEF 规范（SIEM） | （见 `observability/siem/`） | PII 命中事件可推 SIEM |
| 实施计划 B2 | `docs/产品方案/2026-06-23-llmgw-implementation-plan.md` §4 B2 | 任务拆解 |
| armor 包 | `services/llm-gateway-go/security/armor/judge.go` | SDP 复用 Judge 抽象 |
| 镜像仓库规则 | `AGENTS.md` §「基础镜像与镜像仓库规则」| 禁止直连 docker.io |

---

## 8. 待 TL 拍板的开放问题

1. **sidecar 容器 vs 纯 Go 自研**：本文推荐 sidecar，但若团队倾向单二进制部署，需重新评估方案 D（纯 Go 自研，3+ 月工作量）。
2. **FailPolicy 默认值**：`fail_open`（业务优先）还是 `fail_closed`（安全优先）？建议默认 fail_open，高安全租户可选 fail_closed。
3. **spaCy 模型大小**：`zh_core_web_sm`（12MB）vs `zh_core_web_trf`（400MB transformer）。sm 够用但准确率低，trf 准确但资源占用大。建议先 sm，按 FNR 数据决定是否升级。
4. **是否支持图片脱敏**：Presidio 有 `presidio-image-redactor`，但 LLM 网关目前无图片输入。建议 Q4 不做，列入 Q2 2027 backlog。
