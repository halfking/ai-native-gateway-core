# AI Native Gateway（AI 原生閘道）

> 不只是代理。面向 Agent 與 Vibe Coding 時代的 **AI 原生管理平面與工作階段治理平台**。

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8.svg)](https://golang.org)
[![Version](https://img.shields.io/badge/version-2.5.3-green.svg)](CHANGELOG.md)

[English](README.md) | [简体中文](README.zh-CN.md) | **繁體中文** | [日本語](README.ja.md) | [Deutsch](README.de.md) | [Français](README.fr.md) | [Español](README.es.md) | [العربية](README.ar.md)

[快速開始](#quick-start) • [為何 AI 原生](#why-ai-native) • [管理平面](#management-plane) • [工作階段治理](#session-governance) • [功能預覽](#feature-preview) • [對比](#comparison) • [架構](docs/architecture.md)

---

<a id="why-ai-native"></a>

## 為何是 AI 原生閘道？

傳統 API 閘道管的是 HTTP；通用 LLM Proxy 管的是「換一個模型還能用」。
企業把 AI 當作生產力基礎設施之後，真正難的是另外三件事：

1. **管得住** — 誰在用、用了什麼模型、花了多少錢、工作階段有沒有越權
2. **談得完** — Agent 工作階段動輒數十輪、跨模型、跨憑證，上下文不能丟
3. **查得清** — 出了問題能回放整段對話與整條路由決策，而不是一行 access log

AI Native Gateway 把 **工作階段、憑證、租戶、成本、合規** 做成一等物件：一個 Go 二進位內嵌 8 語管理介面，資料留在企業內，對外相容 OpenAI / Anthropic / Gemini / Responses。

| | 傳統 API 閘道 | 通用 LLM Proxy | **AI Native Gateway** |
|---|---|---|---|
| 治理單元 | 請求 / 路徑 | 單次模型呼叫 | **工作階段 + 租戶 + 憑證** |
| 路由 | 上游負載平衡 | fallback / 輪詢 | **雙層：先選模型，再選憑證** |
| 可觀測 | 指標 + 日誌 | Token 統計 | **路由全景 + 工作階段回放 + 成本帳本** |
| 多租戶 | 外掛 / namespace | 應用層隔離 | **PostgreSQL RLS（38+ 表）** |
| 部署 | 元件多、維運重 | SaaS 或腳本 | **單二進位 + 100% 私有化** |

**適用場景**：Vibe Coding / IDE Agent 長工作階段 · 多 Agent 共用出口 · 企業 MaaS / 轉售（套餐 + 積分 + 加油包）· 資料不出域的合規部署。

---

## ✨ 四大核心價值

| 價值 | 客戶感知 |
|------|----------|
| **安全** | AI Guardrails · DLP · Inline Interception · Vibe Coding 治理 · SIEM/SOAR 對接 |
| **穩定** | 多雲編排 · 健康感知熔斷（失敗自動降級、冷卻後恢復）· 99.9% SLA 目標 |
| **低成本** | 語意快取 · 自動路由 · Token 計量 · 提示詞壓縮 · 免費資源池 |
| **企業資源整合** | 全鏈路稽核 · 多租戶 RLS · MaaS 帳本 · MCP / API Hub（路線圖） |

## 🏗️ 三大產品支柱

```
┌────────────────────────┬────────────────────────┬────────────────────────┐
│   Control（管控）       │   Govern（治理）        │   Secure（安全）         │
├────────────────────────┼────────────────────────┼────────────────────────┤
│ ✅ Token 用量追蹤        │ 🔨 API Hub 資產中心     │ 🔨 Model Armor          │
│ ✅ 智慧路由 + 黏性工作階段 │ 🔨 自動發現             │ 🔨 敏感資料脫敏 (SDP)   │
│ ✅ 語意快取 + Funnel    │ 🔨 SpecBoost 智慧富集   │ 🔨 對抗性提示詞防護     │
│ ✅ 全鏈路稽核 + OTel    │ ✅ 多租戶 RLS (L1=0)    │ ✅ SIEM/SOAR 對接       │
│ ✅ MaaS 計費            │ ✅ 工作階段生命週期 / 封存 │ ✅ 金鑰脫敏落庫         │
└────────────────────────┴────────────────────────┴────────────────────────┘
```

✅ = 已上線 &nbsp;·&nbsp; 🔨 = 路線圖中

## 🎯 目前能力

| 能力維度 | 實作 |
|----------|------|
| **協定層** | OpenAI / Anthropic / Gemini / Responses 相容 + SSE 串流中繼（增量完整性校驗） |
| **路由層** | 雙層路由（模型 → 憑證）+ 黏性工作階段 + 健康感知切換；cost/quality 評分目前為 shadow |
| **管理平面** | 內嵌 Vue SPA（8 語）+ 熱設定（約 5 秒生效）+ 憑證矩陣 + 路由全景 + MaaS |
| **工作階段治理** | 黏性綁定 + 預處理壓縮 + 全文回放 + 工作階段摘要 + 4 檔生命週期 |
| **多租戶** | 身分隧道（virtual IP/MAC/ClientID）+ 憑證池 + 38+ 表 RLS |
| **流量治理** | Token 限流（TPM/RPM）+ 語意快取 + 提示詞壓縮 + 滑窗演算法 |
| **稽核** | 全鏈路稽核 + DLQ + 磁碟回退 + OTel + Prometheus |
| **憑證** | 多憑證 + 指紋池（50+ UA · 35 Accept-Language · 11 uTLS）+ 自適應探測 |
| **部署** | 雙實例（Docker + k3s NodePort）共用同一 PostgreSQL schema |

詳細架構見 [架構文件](docs/architecture.md)。

---

<a id="management-plane"></a>

## 🎛️ 管理平面

大多數閘道把「管理」留在設定檔或外部 SaaS。AI Native Gateway 的控制面與資料面同行程：開啟 `/admin` 就能改策略、看工作階段、管租戶、核成本。

- **策略熱更新**：工作類型、路由權重、限流與壓縮閾值約 5 秒生效，無需重啟
- **憑證營運**：19×18 可用性矩陣、P95 / 1h 成功率、並發槽位；失敗熔斷、冷卻恢復
- **路由可觀測**：L1 任務分類（10 類工作類型）→ 6 維評分 → Profile 鎖定；L2 Tier 回退 → 計費輪次 → P2C → 執行 / 熔斷
- **租戶與 MaaS**：使用者 / 金鑰 / 配額一體；套餐 + 積分 + 加油包；CNY + USD；實測單租戶 7 天 14,208 請求 / 7.26 億 Token / $295.50
- **成本目錄**：1045 個 Offer、410 個模型 100% 覆蓋；依憑證 × 模型看已定價 / 免費 / 缺價
- **8 語介面**：管理端與錯誤訊息支援 en / zh-CN / zh-TW / ja / de / fr / es / ar

---

<a id="session-governance"></a>

## 🧠 工作階段治理

通用代理把每次呼叫當成孤立事件。Agent 時代一次「請求」往往只是數十輪對話裡的一跳。本閘道把 **工作階段** 當作治理單元。

- **黏性綁定**：工作階段釘在憑證上，模型切換與故障轉移後上下文仍在
- **預處理流水線**：raw → 脫敏 → 壓縮；接近上游真實視窗約 80% 時自動壓縮
- **長工作階段生存**：依錯誤分級重試 / 換憑證 / 換模型；首位元組後保持串流一致；等待期送協定心跳
- **工作階段稽核與回放**：system prompt + 回應全文留存（生產 13,000+ 工作階段）
- **中繼資料智慧**：10 類工作類型、專案歸屬、標題擷取、摘要
- **身分隧道**：virtual IP/MAC/ClientID 把共用出口上的 Agent 流量歸到租戶
- **4 檔生命週期**：熱 0–7 天 / 溫 7–30 天 / 冷 30–90 天 / 過期 >90 天；封存預覽先告知將動多少筆

---

<a id="feature-preview"></a>

## 🎛️ 產品功能預覽

以下模組均已上線。截圖來自真實本機部署（1728×1050，全量資料載入後）。

### 1. 憑證監控

![憑證監控](docs/assets/screenshots/credential-monitor.png)

19 憑證 × 18 模型二維可用性；P95、1 小時成功率、並發槽位一目了然。

### 2. 路由全景

![路由全景](docs/assets/screenshots/routing-panorama.png)

任務 × 模型熱力圖回答「什麼任務該用什麼模型」；Sankey 展示 14,000+ 請求的最終去向。

### 3. 請求日誌 — 工作階段級稽核

![請求詳情](docs/assets/screenshots/request-detail.png)

Q&A 回放、分發瀑布、路由與重試軌跡、壓縮 / 脫敏紀錄、Token 與快取統計。金鑰以 `sk-****` 落庫。

### 4. 即時請求流 · 工作類型 · 免費池

![即時請求流](docs/assets/screenshots/dashboard-request-stream.png)

![工作類型](docs/assets/screenshots/work-types.png)

![免費資源池](docs/assets/screenshots/free-pool.png)

依處理佇列看 in-flight / p50 / p95；10 類工作類型可在管理端設定；Groq / Google AI Studio / OpenRouter / SiliconFlow / 智譜等免費池可設優先級。

### 5. 資料生命週期 · 租戶計費 · 成本目錄

熱 / 溫 / 冷 / 過期自動分層，封存先預覽再執行。MaaS 子選單涵蓋標準模型、套餐儲值、消耗統計、錢包與帳本。

---

<a id="quick-start"></a>

## 🚀 快速開始

### 方式 A：Docker Compose（建議，10 分鐘內跑通）

```bash
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git ai-native-gateway
cd ai-native-gateway
# GitHub 鏡像：git clone https://github.com/halfking/ai-native-gateway-core.git

cp .env.quickstart.example .env
docker compose -f docker-compose.quickstart.yml up -d
curl http://localhost:8781/healthz
open http://localhost:8781/admin
```

### 方式 B：原始碼建置

```bash
go build -o gateway ./cmd/gateway
LLM_GATEWAY_CONFIG_FILE=config.example.yaml ./gateway
curl http://localhost:8781/healthz
```

開發者建議安裝鉤子：`./scripts/install-githooks.sh --pre-commit` 以及 `./scripts/install-githooks.sh`。

詳細步驟見 [入門指南](docs/getting-started.md)。

---

## 部署模式

| 模式 | 說明 | 狀態 |
|------|------|------|
| **Docker Compose** | 一鍵啟動含 PostgreSQL / Redis 的完整堆疊 | ✅ 評估建議 |
| **二進位 + systemd** | Linux 主機生產部署 | ✅ 提供 installer |
| **Kubernetes** | Deployment + ConfigMap + Service | ⚠️ 測試級（內部生產跑在 k3s） |

快速啟動堆疊與生產使用 **同一二進位、同一 schema**。上生產只需切到託管 PostgreSQL 14+ / Redis 7+ 並加 TLS。

詳見 [生產部署文件](docs/06-deployment/)。

### Installer 一鍵安裝器（`installer/`）

`installer/` 子樹提供 **單一跨平台 Go 二進位**（`llm-gw-installer`），將完整部署流程封裝為 13 步互動式嚮導。開箱支援 Windows / Linux / macOS / 國產 OS / 國產 CPU。

**子命令**

```bash
llm-gw-installer doctor      # 偵測 OS / docker / 網路 / 連接埠
llm-gw-installer install     # 一鍵安裝並部署
llm-gw-installer uninstall   # 解除安裝（--purge 同時清除資料）
```

**跨平台編譯**

```bash
GOOS=linux  GOARCH=amd64   go build -o dist/llm-gw-installer-linux-amd64   ./installer/cmd/llm-gw-installer/
GOOS=linux  GOARCH=arm64   go build -o dist/llm-gw-installer-linux-arm64   ./installer/cmd/llm-gw-installer/
GOOS=linux  GOARCH=loong64 go build -o dist/llm-gw-installer-linux-loong64 ./installer/cmd/llm-gw-installer/
GOOS=darwin GOARCH=amd64   go build -o dist/llm-gw-installer-darwin-amd64  ./installer/cmd/llm-gw-installer/
GOOS=darwin GOARCH=arm64   go build -o dist/llm-gw-installer-darwin-arm64  ./installer/cmd/llm-gw-installer/
GOOS=windows GOARCH=amd64  go build -o dist/llm-gw-installer-windows-amd64.exe ./installer/cmd/llm-gw-installer/
GOOS=windows GOARCH=arm64  go build -o dist/llm-gw-installer-windows-arm64.exe ./installer/cmd/llm-gw-installer/
```

#### 儲存模式（full vs lite）

安裝器內建 **兩種儲存模式**，安裝時二選一：

| 模式 | 儲存後端 | 適用場景 | 安裝時拉取的映像 | 是否初始化 schema |
|------|----------|----------|------------------|---------------------|
| **`full`**（預設） | PostgreSQL (kx-citus) + Redis | 生產 / 多副本 / 高併發 | `kx-llm-gateway-go` + `kx-citus` + `kx-redis` | 是（等待 PG ready + `InitSchema`） |
| **`lite`** | SQLite + 本機 | 單機 / 開發 / CI / 展示 | 僅 `kx-llm-gateway-go` | 否（SQLite 自動建表） |

**選擇優先順序**

1. CLI flag：`--mode lite` 或 `--mode full`（最高優先）
2. 設定檔（`--config /path/to/install.env`）：
   ```
   STORAGE_MODE=lite
   LLM_GATEWAY_MASTER_URL=https://llm.kxpms.cn
   INSTALL_SKIP_ACTIVATION=0
   ```
3. 互動式嚮導：提示 `[1] full  [2] lite`，預設 `1`

非 TTY（CI / `--skip-prompt`）且未提供 `--config` 時，預設 `full`。

**`lite` 模式 install 行為差異**

| 步驟 | `full` | `lite` |
|------|--------|--------|
| 1. 環境偵測 | 同 | 同 |
| 2. 設定（wizard / config） | 同 | 同（新增 storage mode / master URL） |
| 3. 拉取映像 | `kx-citus` + `kx-redis` + `kx-llm-gateway-go` | **僅 `kx-llm-gateway-go`** |
| 4. 寫 `.env` | 全部鍵 | 同 full，僅 `LLM_GATEWAY_STORAGE_MODE=lite` |
| 5. 目錄結構 | 同 | 同（db/data、redis/data 目錄仍會建立但不使用） |
| 6. `compose.yml` | 完整 3 服務 | **剝離 `kx-citus` + `kx-redis`**；gateway 移除 `depends_on` 與 PG/Redis env |
| 7. 啟動容器 | 3 容器 | **僅 `kx-llm-gateway-go`** |
| 8. 初始化資料庫 | 等待 PG ready + `InitSchema`（700+ 遷移） | **跳過**（SQLite 由 app 自動建表） |
| 9. 健康檢查 | 5 項全檢 | 僅驗容器 + `/healthz`；PG/Redis/Schema 不適用，報告中強制顯示 ✅ |

**新增 install flags**

```
--mode string         # full | lite（空 → 走 wizard / 預設 full）
--master-url string   # 主控端 URL（預設 https://llm.kxpms.cn）
--skip-activation     # bool，跳過 install 結尾的自動啟動
```

**新增 `.env` 鍵**（寫入 `{installDir}/.env`）

| Key | 預設 | 說明 |
|-----|------|------|
| `LLM_GATEWAY_STORAGE_MODE` | `full` | 執行時由 `cmd/gateway` 的 `storage_mode_init` 讀取；`lite` 走 SQLite，`full` 走 PG/Redis |
| `LLM_GATEWAY_MASTER_URL` | `https://llm.kxpms.cn` | license 啟動 + 心跳回報的目標 URL |
| `INSTALL_SKIP_ACTIVATION` | `0` | 是否跳過 install 結尾的自動註冊啟動呼叫（邏輯見 `activation.RunAutoActivate`） |

#### 映像源 fallback 鏈

所有容器映像透過 4 層 fallback 鏈拉取，無論在線、內網代理或完全離線都能裝：

```
[1] 離線包 images/*.tar.gz（最高優先）
    ↓ 失敗
[2] registry.kxpms.cn（內部 registry）
    ↓ 失敗
[3] registry.cn-hangzhou.aliyuncs.com（阿里雲 mirror）
    ↓ 失敗
[4] registry-1.docker.io（官方 docker hub）
    ↓ 全部失敗
❌ 清晰報錯
```

**環境變數覆寫**

| 變數 | 預設值 | 用途 |
|---|---|---|
| `KX_REGISTRY` | `registry.kxpms.cn` | 自訂內部 registry |
| `KX_REGISTRY_USERNAME` / `_PASSWORD` | 空 | registry 帳號 / 密碼 |
| `KX_REGISTRY_INSECURE` | `false` | 允許 HTTP |
| `APP_IMAGE_TAG` | 從 MANIFEST 讀 | 應用映像 tag 覆寫 |
| `GOPROXY` | `https://goproxy.cn,direct` | Go module 代理 |

#### 已知限制

- **HarmonyOS NEXT**：不支援（沒有 Linux 容器支援）
- **macOS**：需使用者手動安裝 OrbStack 或 Docker Desktop
- **Windows**：需使用者手動安裝 Docker Desktop + WSL2

Installer 完整設計見 [installer/README.md](installer/README.md)。

---

<a id="comparison"></a>

## 📐 差異化定位與競品對比

| 維度 | 通用 AI Gateway | AI Native Gateway |
|------|-----------------|-------------------|
| **部署 / 資料** | SaaS 或資料出域 | 100% 私有，資料留在企業內 |
| **治理單元** | 請求級日誌 | 黏性工作階段 + 全文回放 + 壓縮 + 4 檔生命週期 |
| **路由** | fallback / 輪詢 | 雙層路由 + 健康感知 + 管理端可觀測 |
| **計費** | 用量計費（USD） | 套餐 + 積分 + 加油包，適合中國 SMB，支付寶 |
| **上游** | 少數廠商 | 全模型 + 國產 + 本地 + 免費池 |
| **指紋池** | 基礎 | 50+ UA · 35 Accept-Language · 11 uTLS |
| **多租戶稽核** | 標準 | 38+ 表 RLS · 租戶稽核 L1=0 |
| **MCP 工具閘道** | 部分 | 2026 Q3 全量交付 |

| 特性 | AI Native Gateway | LiteLLM | OmniRoute | Portkey | Kong AI |
|------|-------------------|---------|-----------|---------|---------|
| **部署** | 私有化自部署 | SaaS + OSS | 自託管（Node） | SaaS | OSS |
| **多租戶** | 原生 PG RLS | 基礎 | 單機導向 | 完整（SaaS） | 外掛 |
| **管理介面** | 內嵌 Vue SPA（8 語） | CLI | Web UI | SaaS UI | Kong Manager |
| **資料駐留** | 100% 私有 | 視模式 | 100% 私有 | 雲端 | 可自託管 |
| **授權** | Apache 2.0 | MIT | 見上游 | 商業專有 | Apache 2.0 |

**選我們**：資料不出域、資料庫級多租戶、工作階段級治理與取證、單二進位內嵌管理面。
**選別人**：最大供應商覆蓋 → LiteLLM；Node.js + 嵌入式庫 → OmniRoute；零維運 SaaS → Portkey；通用 API 閘道 + LLM → Kong。

詳見 [詳細對比](docs/comparison.md)。

---

## 路線圖

**目前（v2.5.x）**：協定相容 · RLS 多租戶 · 雙層路由 + 黏性工作階段 · 壓縮 / 回放 / 生命週期 · Vue 管理台 · Docker Compose

**近期（3–6 個月）**：把 cost/quality 感知路由從 shadow 推到預設可選 · 生產級 Helm · Grafana 模板 · 決策回放

**探索中**：MCP 工具閘道（2026 Q3）· A2A · Kubernetes Operator

完整路線圖見 [ROADMAP.md](ROADMAP.md)。

---

## 📚 文件索引

| 類別 | 文件 |
|------|------|
| 入門 / 架構 / API | [getting-started](docs/getting-started.md) · [architecture](docs/architecture.md) · [API](docs/03-design/01-architecture/architecture/API.md) |
| 對比 / 總覽 | [comparison](docs/comparison.md) · [PROJECT_OVERVIEW](docs/PROJECT_OVERVIEW.md) · [INDEX](docs/INDEX.md) |
| 工作階段手冊 | [session-management](docs/04-implementation/deliverables/user-guide/session-management.md) |
| 雙倉庫 / 安全 / 法務 | [REPO-MIRROR-POLICY](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md) · [SECURITY.md](SECURITY.md) · [disguise-compliance](docs/02-resources/compliance/legal/disguise-compliance.md) |
| 調研 | [A2A](docs/03-design/01-architecture/architecture/a2a-spec-2027.md) · [Armor/SDP](docs/03-design/01-architecture/architecture/armor-sdp-feasibility.md) |

---

## 🔀 雙倉庫策略

| Remote | URL | 用途 |
|--------|-----|------|
| `codeup`（origin） | `https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git` | 日常開發 |
| `github` | `git@github.com:halfking/ai-native-gateway-core.git` | 公開鏡像（階段發布） |

```bash
git push              # → codeup
git push github       # → github（嚴格掃描 49 規則，命中即阻斷）
```

詳見[雙倉庫策略](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md)。

## 🤝 貢獻

見 [CONTRIBUTING.md](CONTRIBUTING.md)。多租戶改動必跑 `lint-tenant-scope-llmgw` / `lint-pg-rls` / `lint-otel-tenant`。

## 🔐 安全

漏洞回報見 [SECURITY.md](SECURITY.md)。公開鏡像掃描見雙倉庫策略。偽裝白名單見 [disguise-compliance](docs/02-resources/compliance/legal/disguise-compliance.md)。

## 授權

[Apache License 2.0](LICENSE)。再散布須保留版權聲明與 [NOTICE](NOTICE)。

---

由 AI Native Gateway 社群用 ❤️ 打造
