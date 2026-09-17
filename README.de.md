# AI Native Gateway

> Kein bloßer Proxy. Eine **AI-native Management-Ebene und Session-Governance-Plattform** für das Agent- und Vibe-Coding-Zeitalter.

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8.svg)](https://golang.org)
[![Version](https://img.shields.io/badge/version-2.5.3-green.svg)](CHANGELOG.md)

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja.md) | **Deutsch** | [Français](README.fr.md) | [Español](README.es.md) | [العربية](README.ar.md)

[Schnellstart](#quick-start) • [Warum AI-native](#why-ai-native) • [Management-Ebene](#management-plane) • [Session-Governance](#session-governance) • [Vorschau](#feature-preview) • [Vergleich](#comparison) • [Architektur](docs/architecture.md)

---

<a id="why-ai-native"></a>

## Warum ein AI-natives Gateway?

Ein klassisches API-Gateway steuert HTTP. Ein generischer LLM-Proxy macht „Modell tauschen, SDK behalten“ möglich.
Sobald KI Produktionsinfrastruktur ist, sind die harten Probleme andere:

1. **Steuern** — wer ruft auf, welches Modell lief, was es kostete, ob eine Session eine Tenant-Grenze überschritt
2. **Zu Ende führen** — Agent-Sessions dauern Dutzende Turns über Modelle und Credentials; Kontext darf nicht verloren gehen
3. **Nachvollziehen** — Sie brauchen das volle Gespräch und die Routing-Entscheidung, nicht eine Access-Log-Zeile

AI Native Gateway behandelt **Sessions, Credentials, Tenants, Kosten und Compliance** als First-Class-Objekte: eine Go-Binary mit 8-sprachiger Admin-UI, Daten bleiben im eigenen Perimeter, Protokolle bleiben OpenAI- / Anthropic- / Gemini- / Responses-kompatibel.

| | Klassisches API-GW | Generischer LLM-Proxy | **AI Native Gateway** |
|---|---|---|---|
| Governance-Einheit | Request / Pfad | Einzelner Modellaufruf | **Session + Tenant + Credential** |
| Routing | Upstream-LB | Fallback / Round-Robin | **Zwei Schichten: erst Modell, dann Credential** |
| Observability | Metriken + Logs | Token-Zähler | **Routing-Panorama + Session-Replay + Ledger** |
| Multi-Tenancy | Plugin / Namespace | Anwendungsebene | **PostgreSQL-RLS auf 38+ Tabellen** |
| Betrieb | Viele Teile | SaaS oder Skripte | **Eine Binary + 100 % privat** |

**Passend für**: lange Vibe-Coding- / IDE-Agent-Sessions · Multi-Agent-Flotten hinter einem gemeinsamen Egress · Enterprise-MaaS / Reseller (Tarife + Credits + Booster) · Data-Residency.

---

## ✨ Vier Kernwerte

| Wert | Kundennutzen |
|------|----------------|
| **Sicher** | AI Guardrails · DLP · Inline Interception · Vibe-Coding-Governance · SIEM/SOAR |
| **Stabil** | Multi-Cloud-Orchestrierung · Health-aware Circuit Breaker · 99,9 % SLA-Ziel |
| **Günstig** | Semantischer Cache · Auto-Routing · Token-Metering · Prompt-Kompression · Free-Pool |
| **Enterprise** | Full-Chain-Audit · Multi-Tenant-RLS · MaaS-Ledger · MCP / API Hub (Roadmap) |

## 🏗️ Drei Produktsäulen

```
┌────────────────────────┬────────────────────────┬────────────────────────┐
│   Control              │   Govern               │   Secure               │
├────────────────────────┼────────────────────────┼────────────────────────┤
│ ✅ Token-Metering       │ 🔨 API-Hub-Assets      │ 🔨 Model Armor         │
│ ✅ Smart Routing+Sticky │ 🔨 Auto-Discovery      │ 🔨 SDP-Redaktion       │
│ ✅ Semantic Cache+Funnel│ 🔨 SpecBoost           │ 🔨 Adversarial Prompts │
│ ✅ Full-Chain-Audit+OTel│ ✅ Multi-Tenant-RLS    │ ✅ SIEM/SOAR           │
│ ✅ MaaS-Billing         │ ✅ Session-Lebenszyklus│ ✅ Secret Masking      │
└────────────────────────┴────────────────────────┴────────────────────────┘
```

✅ = Ausgeliefert &nbsp;·&nbsp; 🔨 = Roadmap

## 🎯 Fähigkeitsmatrix

| Schicht | Leistung |
|---------|----------|
| **Protokoll** | OpenAI / Anthropic / Gemini / Responses + SSE-Relay mit Integritätschecks |
| **Routing** | Zwei Schichten (Modell → Credential) + Sticky Sessions + Health-Failover; Cost/Quality-Scoring ist heute Shadow |
| **Management** | Eingebettete Vue-SPA (8 Locales) + Hot-Config (~5 s) + Credential-Matrix + Routing-Panorama + MaaS |
| **Session-Governance** | Sticky Bind + Vorverarbeitung + Replay + Digest + 4-stufiger Lebenszyklus |
| **Multi-Tenancy** | Identity-Tunnel (virtuelle IP/MAC/ClientID) + Credential-Pools + RLS auf 38+ Tabellen |
| **Traffic** | TPM/RPM + semantischer Cache + Prompt-Kompression + Sliding Windows |
| **Audit** | Full-Chain-Audit + DLQ + Disk-Fallback + OTel + Prometheus |
| **Credentials** | Multi-Credential + Fingerprint-Pool (50+ UA · 35 Accept-Language · 11 uTLS) + adaptives Probing |
| **Betrieb** | Zwei Instanzen (Docker + k3s NodePort) teilen ein PostgreSQL-Schema |

Siehe [Architektur](docs/architecture.md).

---

<a id="management-plane"></a>

## 🎛️ Management-Ebene

Die meisten Gateways lassen „Management“ in Config-Dateien oder externem SaaS. Hier läuft die Control Plane im selben Prozess wie die Data Plane: `/admin` öffnen, Policies ändern, Sessions prüfen, Tenants führen, Kosten abstimmen.

- **Hot Policy**: Work-Types, Routing-Gewichte, Limits und Kompressionsschwellen greifen in ~5 Sekunden — ohne Neustart
- **Credential-Ops**: 19×18-Verfügbarkeitsmatrix, P95 / 1-Stunden-Erfolgsrate, Concurrency-Slots
- **Sichtbares Routing**: L1 klassifiziert 10 Work-Types → 6-D-Score → Profil-Lock; L2 Tier-Fallback → Billing-Runde → P2C → Execute / Break
- **Tenants + MaaS**: User / Keys / Quotas an einem Ort; Tarife + Credits + Booster; CNY + USD; Produktionsbeispiel: 14.208 Requests / 726 Mio. Tokens / 295,50 $ in 7 Tagen auf einem Tenant
- **Kostenkatalog**: 1.045 Offers, 410 Modelle mit 100 % Abdeckung
- **8 UI-Sprachen**: en / zh-CN / zh-TW / ja / de / fr / es / ar

---

<a id="session-governance"></a>

## 🧠 Session-Governance

Ein generischer Proxy behandelt jeden Call isoliert. Im Agent-Zeitalter ist ein „Request“ meist ein Hop in einem langen Gespräch. Dieses Gateway steuert die **Session**.

- **Sticky Binding**: Session an ein Credential pinnen, damit Kontext Modellwechsel und Failover überlebt
- **Preprocess**: raw → redigieren → komprimieren; bei ~80 % des echten Provider-Fensters automatisch komprimieren
- **Lange Sessions**: Retry / Credential-Wechsel / Modellwechsel nach Fehlerklasse; nach dem ersten Byte Stream-Konsistenz; Heartbeats in der Wartezeit
- **Audit & Replay**: voller System-Prompt + Antwort (13.000+ Sessions in Produktion)
- **Metadaten**: 10 Work-Types, Projektzuordnung, Titel, Digest
- **Identity-Tunnel**: virtuelle IP/MAC/ClientID ordnet Shared-Egress-Traffic einem Tenant zu; RLS bleibt gültig
- **4-stufiger Lebenszyklus**: heiß 0–7 d / warm 7–30 d / kalt 30–90 d / abgelaufen >90 d, mit Archiv-Vorschau

---

<a id="feature-preview"></a>

## 🎛️ Produktvorschau

Alle Module sind ausgeliefert. Screenshots von einem lokalen Live-Deploy (1728×1050, nach vollem Datenload).

### 1. Credential-Monitor

![Credential Monitor](docs/assets/screenshots/credential-monitor.png)

19 Credentials × 18 Modelle. P95, 1-Stunden-Erfolg, Concurrency-Slots auf einen Blick.

### 2. Routing-Panorama

![Routing Panorama](docs/assets/screenshots/routing-panorama.png)

Task×Modell-Heatmap und Sankey für 14.000+ Live-Requests (Task → Modell → Provider).

### 3. Request-Logs — Session-Forensik

![Request Detail](docs/assets/screenshots/request-detail.png)

Q&A-Replay, Dispatch-Waterfall, Routing-/Retry-Spur, Kompression/Maskierung, Token- und Cache-Statistik. Keys als `sk-****`.

### 4. Livestream · Work-Types · Free-Pool

![Dashboard Request Stream](docs/assets/screenshots/dashboard-request-stream.png)

![Work Types](docs/assets/screenshots/work-types.png)

![Free Pool](docs/assets/screenshots/free-pool.png)

In-flight / p50 / p95 je Queue. Zehn Work-Types im Admin konfigurierbar. Free-Pools von Groq / Google AI Studio / OpenRouter / SiliconFlow / Zhipu.

### 5. Lebenszyklus · Tenant-Billing · Preiskatalog

Hot/Warm/Cold/Expired mit Vorschau vor dem Archiv. MaaS deckt Katalog, Tarife, Verbrauch, Wallet und Ledger ab.

---

<a id="quick-start"></a>

## 🚀 Schnellstart

### Option A: Docker Compose (empfohlen, < 10 Minuten)

```bash
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git ai-native-gateway
cd ai-native-gateway
# GitHub-Spiegel: git clone https://github.com/halfking/ai-native-gateway-core.git

cp .env.quickstart.example .env
docker compose -f docker-compose.quickstart.yml up -d
curl http://localhost:8781/healthz
open http://localhost:8781/admin
```

### Option B: Aus dem Quellcode

```bash
go build -o gateway ./cmd/gateway
LLM_GATEWAY_CONFIG_FILE=config.example.yaml ./gateway
curl http://localhost:8781/healthz
```

Entwickler: `./scripts/install-githooks.sh --pre-commit` und `./scripts/install-githooks.sh`.

Siehe [Getting Started](docs/getting-started.md).

---

## Betriebsmodi

| Modus | Beschreibung | Status |
|-------|--------------|--------|
| **Docker Compose** | Voller Stack mit PostgreSQL / Redis | ✅ Evaluation |
| **Binary + systemd** | Linux-Produktion | ✅ Installer |
| **Kubernetes** | Deployment + ConfigMap + Service | ⚠️ Teststufe (interne Prod ist k3s) |

Quickstart nutzt **dieselbe Binary und dasselbe Schema** wie Produktion. Live gehen heißt: managed PostgreSQL 14+ / Redis 7+ und TLS — keine Semantikänderung.

Siehe [Production Deployment](docs/06-deployment/).

---

<a id="comparison"></a>

## 📐 Differenzierung & Vergleich

| Dimension | Generisches AI-Gateway | AI Native Gateway |
|-----------|------------------------|-------------------|
| **Betrieb / Daten** | SaaS oder Daten verlassen den Perimeter | 100 % privat |
| **Governance** | Request-Logs | Sticky Session + Replay + Kompression + 4-stufiger Lebenszyklus |
| **Routing** | Fallback / Round-Robin | Zwei Schichten + health-aware + im Admin sichtbar |
| **Billing** | Usage (USD) | Tarife + Credits + Booster (China-SMB, Alipay) |
| **Upstreams** | Wenige Große | Breiter Katalog + chinesische + lokale + Free-Pool |
| **Fingerprint** | Basis | 50+ UA · 35 Accept-Language · 11 uTLS |
| **Tenant-Audit** | Standard | RLS auf 38+ Tabellen · Audits L1 = 0 |
| **MCP-Tool-GW** | Teilweise | Volle Lieferung Q3 2026 |

| Feature | AI Native Gateway | LiteLLM | OmniRoute | Portkey | Kong AI |
|---------|-------------------|---------|-----------|---------|---------|
| **Betrieb** | Privat self-host | SaaS + OSS | Self-host (Node) | SaaS | OSS |
| **Multi-Tenancy** | Natives PG-RLS | Basis | Single-Node | Voll (SaaS) | Plugins |
| **Admin-UI** | Eingebettete Vue-SPA (8 Locales) | CLI | Web-UI | SaaS-UI | Kong Manager |
| **Residency** | 100 % privat | Je nach Modus | 100 % privat | Cloud | Self-hostbar |
| **Lizenz** | Apache 2.0 | MIT | Upstream | Proprietär | Apache 2.0 |

**Wir**, wenn Residency, DB-Tenancy, Session-Forensik und eine Single-Binary-Admin-Ebene zählen.
**Andere**, wenn 100+ Provider (LiteLLM), Node + Embedded-DB (OmniRoute), Zero-Ops-SaaS (Portkey) oder allgemeines API-GW + LLM (Kong) zählen.

Siehe [Detailed Comparison](docs/comparison.md).

---

## Roadmap

**Jetzt (v2.5.x)**: Protokollkompatibilität · RLS · Zwei-Schichten-Routing + Sticky · Kompression / Replay / Lebenszyklus · Vue-Admin · Docker Compose

**Als Nächstes (3–6 Monate)**: Cost/Quality-Routing von Shadow zu opt-in Default · Production-Helm · Grafana-Templates · Decision-Replay

**Exploration**: MCP-Tool-Gateway (Q3 2026) · A2A · Kubernetes Operator

Siehe [ROADMAP.md](ROADMAP.md).

---

## 📚 Dokumentation

| Kategorie | Dokumente |
|-----------|-----------|
| Start / Architektur / API | [getting-started](docs/getting-started.md) · [architecture](docs/architecture.md) · [API](docs/03-design/01-architecture/architecture/API.md) |
| Vergleich / Überblick | [comparison](docs/comparison.md) · [PROJECT_OVERVIEW](docs/PROJECT_OVERVIEW.md) · [INDEX](docs/INDEX.md) |
| Session-Handbuch | [session-management](docs/04-implementation/deliverables/user-guide/session-management.md) |
| Dual-Repo / Security / Legal | [REPO-MIRROR-POLICY](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md) · [SECURITY.md](SECURITY.md) · [disguise-compliance](docs/02-resources/compliance/legal/disguise-compliance.md) |
| Research | [A2A](docs/03-design/01-architecture/architecture/a2a-spec-2027.md) · [Armor/SDP](docs/03-design/01-architecture/architecture/armor-sdp-feasibility.md) |

---

## 🔀 Dual-Repository-Strategie

| Remote | URL | Zweck |
|--------|-----|-------|
| `codeup` (origin) | `https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git` | Tägliche Entwicklung |
| `github` | `git@github.com:halfking/ai-native-gateway-core.git` | Öffentlicher Spiegel |

```bash
git push              # → codeup
git push github       # → github (49-Regel-Scan, Block bei Treffer)
```

Siehe [Mirror-Policy](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md).

## 🤝 Mitwirken

Siehe [CONTRIBUTING.md](CONTRIBUTING.md). Multi-Tenant-Änderungen müssen `lint-tenant-scope-llmgw` / `lint-pg-rls` / `lint-otel-tenant` bestehen.

## 🔐 Sicherheit

Schwachstellen: [SECURITY.md](SECURITY.md). Public-Mirror-Scan: Dual-Repo-Policy. Disguise-Whitelist: [disguise-compliance](docs/02-resources/compliance/legal/disguise-compliance.md).

## Lizenz

[Apache License 2.0](LICENSE). Bei Weitergabe Copyright-Hinweise und [NOTICE](NOTICE) behalten.

---

Gebaut mit ❤️ von der AI-Native-Gateway-Community
