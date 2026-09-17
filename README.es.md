# AI Native Gateway

> No es solo un proxy. Un **plano de gestión nativo de IA y una plataforma de gobernanza de sesiones** para la era de los agentes y el vibe coding.

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8.svg)](https://golang.org)
[![Version](https://img.shields.io/badge/version-2.5.3-green.svg)](CHANGELOG.md)

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja.md) | [Deutsch](README.de.md) | [Français](README.fr.md) | **Español** | [العربية](README.ar.md)

[Inicio rápido](#quick-start) • [Por qué nativo de IA](#why-ai-native) • [Plano de gestión](#management-plane) • [Gobernanza de sesión](#session-governance) • [Vista previa](#feature-preview) • [Comparación](#comparison) • [Arquitectura](docs/architecture.md)

---

<a id="why-ai-native"></a>

## ¿Por qué una pasarela nativa de IA?

Una API gateway clásica gestiona HTTP. Un proxy LLM genérico permite «cambiar el modelo y conservar el SDK».
Cuando la IA es infraestructura de producción, los problemas difíciles son otros:

1. **Controlar** — quién llama, qué modelo corrió, cuánto costó y si una sesión cruzó el límite del tenant
2. **Terminar** — las sesiones de agentes duran decenas de turnos, cambian de modelo y de credencial; el contexto no puede desaparecer
3. **Investigar** — hace falta la conversación completa y la decisión de enrutado, no una línea de access log

AI Native Gateway trata **sesiones, credenciales, tenants, coste y cumplimiento** como objetos de primer nivel: un binario Go con UI de administración en 8 idiomas, los datos se quedan en su perímetro y los protocolos siguen compatibles con OpenAI / Anthropic / Gemini / Responses.

| | API GW clásica | Proxy LLM genérico | **AI Native Gateway** |
|---|---|---|---|
| Unidad de gobernanza | Petición / ruta | Una llamada a modelo | **Sesión + tenant + credencial** |
| Enrutado | LB de upstream | Fallback / round-robin | **Dos capas: primero modelo, luego credencial** |
| Observabilidad | Métricas + logs | Contadores de tokens | **Panorama de enrutado + replay + libro mayor** |
| Multi-tenant | Plugin / namespace | Capa de aplicación | **RLS de PostgreSQL en 38+ tablas** |
| Despliegue | Muchas piezas | SaaS o scripts | **Un binario + 100 % privado** |

**Encaja con**: sesiones largas de Vibe Coding / agentes IDE · flotas multi-agente en un egress compartido · MaaS / reventa (planes + créditos + boosters) · residencia de datos.

---

## ✨ Cuatro valores centrales

| Valor | Lo que percibe el cliente |
|-------|---------------------------|
| **Seguro** | AI Guardrails · DLP · interceptación inline · gobernanza vibe coding · SIEM/SOAR |
| **Estable** | Orquestación multi-nube · circuit breaker sensible a la salud · objetivo SLA 99,9 % |
| **Bajo coste** | Caché semántica · auto-enrutado · medición de tokens · compresión de prompts · pool gratuito |
| **Integración** | Auditoría de punta a punta · RLS multi-tenant · libro MaaS · MCP / API Hub (hoja de ruta) |

## 🏗️ Tres pilares de producto

```
┌────────────────────────┬────────────────────────┬────────────────────────┐
│   Control              │   Govern               │   Secure               │
├────────────────────────┼────────────────────────┼────────────────────────┤
│ ✅ Medición de tokens   │ 🔨 Activos API Hub     │ 🔨 Model Armor         │
│ ✅ Enrutado + sticky    │ 🔨 Auto-descubrimiento │ 🔨 Redacción SDP       │
│ ✅ Caché semántica      │ 🔨 SpecBoost           │ 🔨 Prompts adversarios │
│ ✅ Auditoría + OTel     │ ✅ RLS multi-tenant    │ ✅ SIEM/SOAR           │
│ ✅ Facturación MaaS     │ ✅ Ciclo de vida       │ ✅ Enmascarado secrets │
└────────────────────────┴────────────────────────┴────────────────────────┘
```

✅ = En producción &nbsp;·&nbsp; 🔨 = Hoja de ruta

## 🎯 Matriz de capacidades

| Capa | Qué obtiene |
|------|-------------|
| **Protocolo** | OpenAI / Anthropic / Gemini / Responses + relé SSE con comprobaciones de integridad |
| **Enrutado** | Dos capas (modelo → credencial) + sesiones sticky + conmutación por salud; el scoring cost/quality está en shadow |
| **Gestión** | SPA Vue embebida (8 locales) + config en caliente (~5 s) + matriz de credenciales + panorama + MaaS |
| **Sesión** | Bind sticky + compresión + replay + digest + ciclo de vida de 4 niveles |
| **Multi-tenant** | Túnel de identidad (IP/MAC/ClientID virtuales) + pools + RLS en 38+ tablas |
| **Tráfico** | Límites TPM/RPM + caché semántica + compresión + ventanas deslizantes |
| **Auditoría** | Auditoría completa + DLQ + fallback a disco + OTel + Prometheus |
| **Credenciales** | Multi-credencial + pool de huellas (50+ UA · 35 Accept-Language · 11 uTLS) + sondeo adaptativo |
| **Despliegue** | Dos instancias (Docker + k3s NodePort) comparten un esquema PostgreSQL |

Véase [Architecture](docs/architecture.md).

---

<a id="management-plane"></a>

## 🎛️ Plano de gestión

La mayoría de las pasarelas dejan la «gestión» en archivos de configuración o en un SaaS externo. Aquí el plano de control viaja en el mismo proceso que el de datos: abra `/admin` para cambiar política, inspeccionar sesiones, operar tenants y cuadrar costes.

- **Política en caliente**: tipos de trabajo, pesos de enrutado, límites y umbrales de compresión en ~5 segundos — sin reiniciar
- **Ops de credenciales**: matriz 19×18, P95 / éxito a 1 h, slots de concurrencia
- **Enrutado observable**: L1 clasifica 10 tipos de trabajo → score de 6 dimensiones → perfil; L2 fallback de tier → ronda de facturación → P2C → ejecutar / cortar
- **Tenants + MaaS**: usuarios / claves / cuotas en un sitio; planes + créditos + boosters; CNY + USD; muestra de prod: 14.208 peticiones / 726 M tokens / 295,50 $ en 7 días en un tenant
- **Catálogo de costes**: 1.045 ofertas, 410 modelos al 100 % de cobertura
- **8 idiomas de UI**: en / zh-CN / zh-TW / ja / de / fr / es / ar

---

<a id="session-governance"></a>

## 🧠 Gobernanza de sesión

Un proxy genérico trata cada llamada como un evento aislado. En la era de los agentes, una «petición» suele ser un salto de una conversación larga. Esta pasarela gobierna la **sesión**.

- **Bind sticky**: fijar la sesión a una credencial para que el contexto sobreviva a cambios de modelo y failovers
- **Preproceso**: raw → redactar → comprimir; cerca del 80 % de la ventana real del proveedor, comprimir antes de enviar
- **Sesiones largas**: reintento / cambiar credencial / cambiar modelo según la clase de error; tras el primer byte, mantener el stream coherente; heartbeats en la espera
- **Auditoría y replay**: system prompt + respuesta completos (13.000+ sesiones en producción)
- **Metadatos**: 10 tipos de trabajo, proyecto, título, digest
- **Túnel de identidad**: IP/MAC/ClientID virtuales atribuyen el tráfico de agentes en un egress compartido a un tenant; el RLS se mantiene
- **Ciclo de vida de 4 niveles**: caliente 0–7 d / templado 7–30 d / frío 30–90 d / caducado >90 d, con vista previa antes de archivar

---

<a id="feature-preview"></a>

## 🎛️ Vista previa del producto

Todos los módulos siguientes están en producción. Capturas de un despliegue local real (1728×1050, tras cargar todos los datos).

### 1. Monitor de credenciales

![Credential Monitor](docs/assets/screenshots/credential-monitor.png)

19 credenciales × 18 modelos. P95, éxito a 1 hora y slots de concurrencia de un vistazo.

### 2. Panorama de enrutado

![Routing Panorama](docs/assets/screenshots/routing-panorama.png)

Mapa de calor tarea × modelo y Sankey de 14.000+ peticiones en vivo (tarea → modelo → proveedor).

### 3. Registros — forense de sesión

![Request Detail](docs/assets/screenshots/request-detail.png)

Replay de Q&A, cascada de dispatch, rastro de enrutado/reintento, compresión/enmascarado, stats de tokens y caché. Claves como `sk-****`.

### 4. Flujo en vivo · tipos de trabajo · pool gratuito

![Dashboard Request Stream](docs/assets/screenshots/dashboard-request-stream.png)

![Work Types](docs/assets/screenshots/work-types.png)

![Free Pool](docs/assets/screenshots/free-pool.png)

In-flight / p50 / p95 por cola. Diez tipos de trabajo configurables. Pools Groq / Google AI Studio / OpenRouter / SiliconFlow / Zhipu.

### 5. Ciclo de vida · facturación tenant · catálogo de precios

Niveles caliente/templado/frío/caducado con vista previa antes de archivar. MaaS cubre catálogo, planes, uso, monedero y libro mayor.

---

<a id="quick-start"></a>

## 🚀 Inicio rápido

### Opción A: Docker Compose (recomendado, < 10 minutos)

```bash
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git ai-native-gateway
cd ai-native-gateway
# Espejo GitHub: git clone https://github.com/halfking/ai-native-gateway-core.git

cp .env.quickstart.example .env
docker compose -f docker-compose.quickstart.yml up -d
curl http://localhost:8781/healthz
open http://localhost:8781/admin
```

### Opción B: Compilar desde el código

```bash
go build -o gateway ./cmd/gateway
LLM_GATEWAY_CONFIG_FILE=config.example.yaml ./gateway
curl http://localhost:8781/healthz
```

Desarrolladores: `./scripts/install-githooks.sh --pre-commit` y `./scripts/install-githooks.sh`.

Véase [Getting Started](docs/getting-started.md).

---

## Modos de despliegue

| Modo | Descripción | Estado |
|------|-------------|--------|
| **Docker Compose** | Stack completo con PostgreSQL / Redis | ✅ Evaluación |
| **Binario + systemd** | Producción Linux | ✅ Instalador |
| **Kubernetes** | Deployment + ConfigMap + Service | ⚠️ Nivel de prueba (la prod interna es k3s) |

El inicio rápido usa el **mismo binario y el mismo esquema** que producción. Pasar a prod es apuntar a PostgreSQL 14+ / Redis 7+ gestionados y añadir TLS.

Véase [Production Deployment](docs/06-deployment/).

---

<a id="comparison"></a>

## 📐 Diferenciación y comparación

| Dimensión | Pasarela IA genérica | AI Native Gateway |
|-----------|----------------------|-------------------|
| **Despliegue / datos** | SaaS o datos fuera del perímetro | 100 % privado |
| **Gobernanza** | Logs de petición | Sesión sticky + replay + compresión + 4 niveles |
| **Enrutado** | Fallback / round-robin | Dos capas + salud + visible en admin |
| **Facturación** | Uso (USD) | Planes + créditos + boosters (PYME China, Alipay) |
| **Upstreams** | Pocos grandes | Catálogo amplio + modelos chinos + locales + pool gratuito |
| **Huellas** | Básico | 50+ UA · 35 Accept-Language · 11 uTLS |
| **Auditoría tenant** | Estándar | RLS en 38+ tablas · auditorías L1 = 0 |
| **Pasarela MCP** | Parcial | Entrega completa prevista en T3 2026 |

| Función | AI Native Gateway | LiteLLM | OmniRoute | Portkey | Kong AI |
|---------|-------------------|---------|-----------|---------|---------|
| **Despliegue** | Self-host privado | SaaS + OSS | Self-host (Node) | SaaS | OSS |
| **Multi-tenant** | RLS PG nativo | Básico | Un nodo | Completo (SaaS) | Plugins |
| **UI admin** | SPA Vue embebida (8 locales) | CLI | Web UI | UI SaaS | Kong Manager |
| **Residencia** | 100 % privado | Depende | 100 % privado | Nube | Autoalojable |
| **Licencia** | Apache 2.0 | MIT | Upstream | Propietaria | Apache 2.0 |

**Elijános** por residencia, multi-tenant a nivel de base de datos, forense de sesión y un plano de admin en un solo binario.
**Elija otros** por 100+ proveedores (LiteLLM), Node + BD embebida (OmniRoute), SaaS cero-ops (Portkey) o API GW general + LLM (Kong).

Véase [comparación detallada](docs/comparison.md).

---

## Hoja de ruta

**Ahora (v2.5.x)**: compatibilidad de protocolos · RLS · enrutado de dos capas + sticky · compresión / replay / ciclo de vida · admin Vue · Docker Compose

**Siguiente (3–6 meses)**: pasar el enrutado cost/quality de shadow a opción por defecto · Helm de producción · plantillas Grafana · replay de decisiones

**Exploración**: pasarela de herramientas MCP (T3 2026) · A2A · Operator de Kubernetes

Véase [ROADMAP.md](ROADMAP.md).

---

## 📚 Documentación

| Categoría | Documentos |
|-----------|------------|
| Inicio / arquitectura / API | [getting-started](docs/getting-started.md) · [architecture](docs/architecture.md) · [API](docs/03-design/01-architecture/architecture/API.md) |
| Comparación / visión | [comparison](docs/comparison.md) · [PROJECT_OVERVIEW](docs/PROJECT_OVERVIEW.md) · [INDEX](docs/INDEX.md) |
| Guía de sesión | [session-management](docs/04-implementation/deliverables/user-guide/session-management.md) |
| Dual-repo / seguridad / legal | [REPO-MIRROR-POLICY](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md) · [SECURITY.md](SECURITY.md) · [disguise-compliance](docs/02-resources/compliance/legal/disguise-compliance.md) |
| Investigación | [A2A](docs/03-design/01-architecture/architecture/a2a-spec-2027.md) · [Armor/SDP](docs/03-design/01-architecture/architecture/armor-sdp-feasibility.md) |

---

## 🔀 Estrategia de doble repositorio

| Remote | URL | Uso |
|--------|-----|-----|
| `codeup` (origin) | `https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git` | Desarrollo diario |
| `github` | `git@github.com:halfking/ai-native-gateway-core.git` | Espejo público |

```bash
git push              # → codeup
git push github       # → github (escaneo estricto de 49 reglas; se bloquea si hay hit)
```

Véase la [política de espejo](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md).

## 🤝 Contribuir

Véase [CONTRIBUTING.md](CONTRIBUTING.md). Los cambios multi-tenant deben pasar `lint-tenant-scope-llmgw` / `lint-pg-rls` / `lint-otel-tenant`.

## 🔐 Seguridad

Vulnerabilidades: [SECURITY.md](SECURITY.md). Escaneo del espejo público: política dual-repo. Lista blanca de disfraz: [disguise-compliance](docs/02-resources/compliance/legal/disguise-compliance.md).

## Licencia

[Apache License 2.0](LICENSE). Conserve los avisos de copyright y [NOTICE](NOTICE) al redistribuir.

---

Hecho con ❤️ por la comunidad AI Native Gateway
