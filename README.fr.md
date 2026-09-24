# AI Native Gateway

> Pas un simple proxy. Un **plan de management et une plateforme de gouvernance de session nativement IA** pour l’ère des agents et du vibe coding.

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8.svg)](https://golang.org)
[![Version](https://img.shields.io/badge/version-2.5.3-green.svg)](CHANGELOG.md)

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja.md) | [Deutsch](README.de.md) | **Français** | [Español](README.es.md) | [العربية](README.ar.md)

[Démarrage](#quick-start) • [Pourquoi natif IA](#why-ai-native) • [Plan de management](#management-plane) • [Gouvernance de session](#session-governance) • [Aperçu](#feature-preview) • [Comparaison](#comparison) • [Architecture](docs/architecture.md)

---

<a id="why-ai-native"></a>

## Pourquoi une passerelle nativement IA ?

Une API gateway classique gère le HTTP. Un proxy LLM générique permet de « changer de modèle, garder le SDK ».
Dès que l’IA devient une infrastructure de production, les vrais problèmes changent :

1. **Contrôler** — qui appelle, quel modèle a tourné, combien ça a coûté, si une session a franchi une frontière de tenant
2. **Aller au bout** — les sessions d’agents durent des dizaines de tours, changent de modèle et de credential ; le contexte ne doit pas disparaître
3. **Retracer** — il faut la conversation entière et la décision de routage, pas une ligne de access log

AI Native Gateway traite **sessions, credentials, tenants, coût et conformité** comme des objets de premier rang : un binaire Go avec une UI d’admin en 8 langues, les données restent dans votre périmètre, les protocoles restent compatibles OpenAI / Anthropic / Gemini / Responses.

| | API GW classique | Proxy LLM générique | **AI Native Gateway** |
|---|---|---|---|
| Unité de gouvernance | Requête / chemin | Un appel modèle | **Session + tenant + credential** |
| Routage | LB amont | Fallback / round-robin | **Deux couches : modèle, puis credential** |
| Observabilité | Métriques + logs | Compteurs de tokens | **Panorama de routage + replay + grand livre** |
| Multi-tenant | Plugin / namespace | Couche applicative | **RLS PostgreSQL sur 38+ tables** |
| Déploiement | Beaucoup de pièces | SaaS ou scripts | **Un binaire + 100 % privé** |

**Cas d’usage** : longues sessions Vibe Coding / agents IDE · flottes multi-agents sur une sortie partagée · MaaS / revendeur (forfaits + crédits + boosters) · résidence des données.

---

## ✨ Quatre valeurs fondamentales

| Valeur | Bénéfice client |
|--------|-----------------|
| **Sécurité** | AI Guardrails · DLP · interception inline · gouvernance vibe coding · SIEM/SOAR |
| **Stabilité** | Orchestration multi-cloud · disjoncteur sensible à la santé · objectif SLA 99,9 % |
| **Coût** | Cache sémantique · auto-routage · metering tokens · compression de prompts · pool gratuit |
| **Intégration** | Audit bout-en-bout · RLS multi-tenant · grand livre MaaS · MCP / API Hub (feuille de route) |

## 🏗️ Trois piliers produit

```
┌────────────────────────┬────────────────────────┬────────────────────────┐
│   Control              │   Govern               │   Secure               │
├────────────────────────┼────────────────────────┼────────────────────────┤
│ ✅ Metering tokens      │ 🔨 Actifs API Hub      │ 🔨 Model Armor         │
│ ✅ Routage + sticky     │ 🔨 Auto-discovery      │ 🔨 Redaction SDP       │
│ ✅ Cache sémantique     │ 🔨 SpecBoost           │ 🔨 Prompts adverses    │
│ ✅ Audit + OTel         │ ✅ RLS multi-tenant    │ ✅ SIEM/SOAR           │
│ ✅ Facturation MaaS     │ ✅ Cycle de vie session│ ✅ Masquage de secrets │
└────────────────────────┴────────────────────────┴────────────────────────┘
```

✅ = Livré &nbsp;·&nbsp; 🔨 = Feuille de route

## 🎯 Matrice de capacités

| Couche | Ce que vous obtenez |
|--------|---------------------|
| **Protocole** | OpenAI / Anthropic / Gemini / Responses + relais SSE avec contrôles d’intégrité |
| **Routage** | Deux couches (modèle → credential) + sessions sticky + bascule santé ; le scoring cost/quality est en shadow |
| **Management** | SPA Vue intégrée (8 locales) + config à chaud (~5 s) + matrice credentials + panorama + MaaS |
| **Session** | Bind sticky + compression + replay + digest + cycle de vie 4 niveaux |
| **Multi-tenant** | Tunnel d’identité (IP/MAC/ClientID virtuels) + pools + RLS sur 38+ tables |
| **Trafic** | Limites TPM/RPM + cache sémantique + compression + fenêtres glissantes |
| **Audit** | Audit bout-en-bout + DLQ + repli disque + OTel + Prometheus |
| **Credentials** | Multi-credential + pool d’empreintes (50+ UA · 35 Accept-Language · 11 uTLS) + sonde adaptive |
| **Déploiement** | Deux instances (Docker + k3s NodePort) partagent un schéma PostgreSQL |

Voir [Architecture](docs/architecture.md).

---

<a id="management-plane"></a>

## 🎛️ Plan de management

La plupart des passerelles laissent le « management » dans des fichiers de config ou un SaaS externe. Ici, le plan de contrôle est dans le même processus que le plan de données : ouvrez `/admin` pour changer une politique, inspecter une session, piloter un tenant et rapprocher les coûts.

- **Politique à chaud** : types de travail, poids de routage, limites et seuils de compression en ~5 secondes — sans redémarrage
- **Ops credentials** : matrice 19×18, P95 / succès sur 1 h, slots de concurrence
- **Routage observable** : L1 classe 10 types de travail → score 6 dimensions → profil ; L2 repli de palier → tour de facturation → P2C → exécuter / couper
- **Tenants + MaaS** : utilisateurs / clés / quotas au même endroit ; forfaits + crédits + boosters ; CNY + USD ; échantillon prod : 14 208 requêtes / 726 M tokens / 295,50 $ en 7 jours sur un tenant
- **Catalogue de coûts** : 1 045 offres, 410 modèles à 100 % de couverture
- **8 langues d’UI** : en / zh-CN / zh-TW / ja / de / fr / es / ar

---

<a id="session-governance"></a>

## 🧠 Gouvernance de session

Un proxy générique traite chaque appel comme un événement isolé. À l’ère des agents, une « requête » n’est souvent qu’un saut dans une longue conversation. Cette passerelle gouverne la **session**.

- **Bind sticky** : épingler la session à un credential pour que le contexte survive aux changements de modèle et aux failovers
- **Prétraitement** : raw → masquage → compression ; vers ~80 % de la fenêtre réelle du fournisseur, compresser avant l’envoi
- **Sessions longues** : retry / changer de credential / changer de modèle selon la classe d’erreur ; après le premier octet, garder le flux cohérent ; heartbeats pendant l’attente
- **Audit & replay** : system prompt + réponse complets (13 000+ sessions en production)
- **Métadonnées** : 10 types de travail, projet, titre, digest
- **Tunnel d’identité** : IP/MAC/ClientID virtuels rattachent le trafic d’agents en egress partagé à un tenant ; le RLS tient
- **Cycle de vie 4 niveaux** : chaud 0–7 j / tiède 7–30 j / froid 30–90 j / expiré >90 j, avec aperçu avant archivage

---

<a id="feature-preview"></a>

## 🎛️ Aperçu produit

Tous les modules ci-dessous sont livrés. Captures d’un déploiement local réel (1728×1050, données chargées).

### 1. Moniteur de credentials

![Credential Monitor](docs/assets/screenshots/credential-monitor.png)

19 credentials × 18 modèles. P95, succès 1 h, slots de concurrence d’un coup d’œil.

### 2. Panorama de routage

![Routing Panorama](docs/assets/screenshots/routing-panorama.png)

Heatmap tâche × modèle et Sankey de 14 000+ requêtes live (tâche → modèle → fournisseur).

### 3. Journaux — forensics de session

![Request Detail](docs/assets/screenshots/request-detail.png)

Replay Q&R, waterfall de dispatch, piste de routage/retry, compression/masquage, stats tokens et cache. Clés stockées en `sk-****`.

### 4. Flux live · types de travail · pool gratuit

![Dashboard Request Stream](docs/assets/screenshots/dashboard-request-stream.png)

![Work Types](docs/assets/screenshots/work-types.png)

![Free Pool](docs/assets/screenshots/free-pool.png)

In-flight / p50 / p95 par file. Dix types de travail configurables. Pools Groq / Google AI Studio / OpenRouter / SiliconFlow / Zhipu.

### 5. Cycle de vie · facturation tenant · catalogue de prix

Niveaux chaud/tiède/froid/expiré avec aperçu avant archivage. MaaS couvre catalogue, forfaits, usage, portefeuille et grand livre.

---

<a id="quick-start"></a>

## 🚀 Démarrage rapide

### Option A : Docker Compose (recommandé, < 10 minutes)

```bash
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git ai-native-gateway
cd ai-native-gateway
# Miroir GitHub : git clone https://github.com/halfking/ai-native-gateway-core.git

cp .env.quickstart.example .env
docker compose -f docker-compose.quickstart.yml up -d
curl http://localhost:8781/healthz
open http://localhost:8781/admin
```

### Option B : Compiler depuis les sources

```bash
go build -o gateway ./cmd/gateway
LLM_GATEWAY_CONFIG_FILE=config.example.yaml ./gateway
curl http://localhost:8781/healthz
```

Développeurs : `./scripts/install-githooks.sh --pre-commit` et `./scripts/install-githooks.sh`.

Voir [Getting Started](docs/getting-started.md).

---

## Modes de déploiement

| Mode | Description | Statut |
|------|-------------|--------|
| **Docker Compose** | Pile complète PostgreSQL / Redis | ✅ Évaluation |
| **Binaire + systemd** | Production Linux | ✅ Installer |
| **Kubernetes** | Deployment + ConfigMap + Service | ⚠️ Niveau test (la prod interne est k3s) |

Le quickstart utilise le **même binaire et le même schéma** que la production. Passer en prod = PostgreSQL 14+ / Redis 7+ gérés + TLS.

Voir [Production Deployment](docs/06-deployment/).

### Installer — déployeur en un clic (`installer/`)

Le sous-arbre `installer/` livre **un unique binaire Go multiplateforme** (`llm-gw-installer`) qui encapsule tout le flux de déploiement dans un wizard interactif en 13 étapes. Windows / Linux / macOS / 国产 OS / 国产 CPU sont supportés sans configuration supplémentaire.

**Sous-commandes**

```bash
llm-gw-installer doctor      # détecte OS / docker / réseau / ports
llm-gw-installer install     # installation + déploiement en un clic
llm-gw-installer uninstall   # désinstallation (--purge supprime aussi les données)
```

**Build multiplateforme**

```bash
GOOS=linux  GOARCH=amd64   go build -o dist/llm-gw-installer-linux-amd64   ./installer/cmd/llm-gw-installer/
GOOS=linux  GOARCH=arm64   go build -o dist/llm-gw-installer-linux-arm64   ./installer/cmd/llm-gw-installer/
GOOS=linux  GOARCH=loong64 go build -o dist/llm-gw-installer-linux-loong64 ./installer/cmd/llm-gw-installer/
GOOS=darwin GOARCH=amd64   go build -o dist/llm-gw-installer-darwin-amd64  ./installer/cmd/llm-gw-installer/
GOOS=darwin GOARCH=arm64   go build -o dist/llm-gw-installer-darwin-arm64  ./installer/cmd/llm-gw-installer/
GOOS=windows GOARCH=amd64  go build -o dist/llm-gw-installer-windows-amd64.exe ./installer/cmd/llm-gw-installer/
GOOS=windows GOARCH=arm64  go build -o dist/llm-gw-installer-windows-arm64.exe ./installer/cmd/llm-gw-installer/
```

#### Modes de stockage (full vs lite)

L’installeur propose **deux modes de stockage** à choisir au moment de l’installation :

| Mode | Backend de stockage | Cas d’usage | Images tirées à l’install | Initialise le schéma ? |
|------|---------------------|-------------|----------------------------|------------------------|
| **`full`** (par défaut) | PostgreSQL (kx-citus) + Redis | Production / multi-réplica / haute concurrence | `kx-llm-gateway-go` + `kx-citus` + `kx-redis` | Oui (attend PG ready + `InitSchema`) |
| **`lite`** | SQLite + local | Mono-machine / dev / CI / démo | `kx-llm-gateway-go` uniquement | Non (SQLite crée les tables) |

**Priorité de sélection**

1. Flag CLI : `--mode lite` ou `--mode full` (priorité la plus haute)
2. Fichier de configuration (`--config /path/to/install.env`) :
   ```
   STORAGE_MODE=lite
   LLM_GATEWAY_MASTER_URL=https://llm.kxpms.cn
   INSTALL_SKIP_ACTIVATION=0
   ```
3. Wizard interactif : propose `[1] full  [2] lite`, par défaut `1`

En non-TTY (CI / `--skip-prompt`) sans `--config`, la valeur par défaut est `full`.

**Comportement de l’install en mode `lite`**

| Étape | `full` | `lite` |
|-------|--------|--------|
| 1. Détection d’environnement | identique | identique |
| 2. Configuration (wizard / config) | identique | identique (ajoute storage mode + master URL) |
| 3. Pull des images | `kx-citus` + `kx-redis` + `kx-llm-gateway-go` | **`kx-llm-gateway-go` uniquement** |
| 4. Écriture de `.env` | toutes les clés | identique à full, seule `LLM_GATEWAY_STORAGE_MODE=lite` |
| 5. Arborescence | complète | complète (db/data, redis/data créés mais inutilisés) |
| 6. `compose.yml` | 3 services | **`kx-citus` + `kx-redis` retirés** ; la gateway perd `depends_on` et l’env PG/Redis |
| 7. Démarrage des conteneurs | 3 conteneurs | **`kx-llm-gateway-go` uniquement** |
| 8. Initialisation de la base | attendre PG ready + `InitSchema` (700+ migrations) | **sauté** (SQLite auto-crée) |
| 9. Vérification de santé | contrôle complet en 5 points | conteneur + `/healthz` uniquement ; PG/Redis/Schema non applicables, ✅ forcé au rapport |

**Nouveaux flags d’install**

```
--mode string         # full | lite (vide → wizard / défaut full)
--master-url string   # URL du plan de contrôle (défaut https://llm.kxpms.cn)
--skip-activation     # bool, saute l’activation automatique en fin d’install
```

**Nouvelles clés `.env`** (écrites dans `{installDir}/.env`)

| Clé | Défaut | Signification |
|-----|--------|---------------|
| `LLM_GATEWAY_STORAGE_MODE` | `full` | Lue à l’exécution par `cmd/gateway` via `storage_mode_init` ; `lite` → SQLite, `full` → PG/Redis |
| `LLM_GATEWAY_MASTER_URL` | `https://llm.kxpms.cn` | URL cible pour l’activation de licence + heartbeat |
| `INSTALL_SKIP_ACTIVATION` | `0` | Sauter l’appel d’auto-enregistrement d’activation en fin d’install (logique dans `activation.RunAutoActivate`) |

#### Chaîne de repli pour les images

Toutes les images conteneurs sont tirées via une chaîne de repli à 4 niveaux pour fonctionner en ligne, derrière un proxy d’entreprise ou totalement air-gap :

```
[1] Bundle hors ligne images/*.tar.gz  (priorité la plus haute)
    ↓ échec
[2] registry.kxpms.cn              (registry interne)
    ↓ échec
[3] registry.cn-hangzhou.aliyuncs.com (miroir Aliyun)
    ↓ échec
[4] registry-1.docker.io           (Docker Hub officiel)
    ↓ tout échoue
❌ erreur claire et exploitable
```

**Surcharges par variables d’environnement**

| Variable | Défaut | Usage |
|----------|--------|-------|
| `KX_REGISTRY` | `registry.kxpms.cn` | Registry interne personnalisé |
| `KX_REGISTRY_USERNAME` / `_PASSWORD` | vide | Identifiants registry |
| `KX_REGISTRY_INSECURE` | `false` | Autoriser HTTP |
| `APP_IMAGE_TAG` | lu depuis MANIFEST | Surcharger le tag d’image de l’app |
| `GOPROXY` | `https://goproxy.cn,direct` | Proxy de modules Go |

#### Limitations connues

- **HarmonyOS NEXT** : non supporté (pas de support de conteneurs Linux)
- **macOS** : l’utilisateur doit installer manuellement OrbStack ou Docker Desktop
- **Windows** : l’utilisateur doit installer manuellement Docker Desktop + WSL2

Pour le design complet de l’installeur, voir [installer/README.md](installer/README.md).

---

<a id="comparison"></a>

## 📐 Différenciation & comparaison

| Dimension | Passerelle IA générique | AI Native Gateway |
|-----------|-------------------------|-------------------|
| **Déploiement / données** | SaaS ou données hors périmètre | 100 % privé |
| **Gouvernance** | Logs de requêtes | Session sticky + replay + compression + 4 niveaux |
| **Routage** | Fallback / round-robin | Deux couches + santé + visible dans l’admin |
| **Facturation** | Usage (USD) | Forfaits + crédits + boosters (PME Chine, Alipay) |
| **Amonts** | Quelques majors | Catalogue large + modèles chinois + locaux + pool gratuit |
| **Empreintes** | Basique | 50+ UA · 35 Accept-Language · 11 uTLS |
| **Audit tenant** | Standard | RLS 38+ tables · audits L1 = 0 |
| **Passerelle MCP** | Partiel | Livraison complète visée T3 2026 |

| Fonction | AI Native Gateway | LiteLLM | OmniRoute | Portkey | Kong AI |
|----------|-------------------|---------|-----------|---------|---------|
| **Déploiement** | Self-host privé | SaaS + OSS | Self-host (Node) | SaaS | OSS |
| **Multi-tenant** | RLS PG natif | Basique | Nœud unique | Complet (SaaS) | Plugins |
| **UI admin** | SPA Vue intégrée (8 locales) | CLI | Web UI | UI SaaS | Kong Manager |
| **Résidence** | 100 % privé | Selon le mode | 100 % privé | Cloud | Self-hostable |
| **Licence** | Apache 2.0 | MIT | Amont | Propriétaire | Apache 2.0 |

**Nous** pour la résidence, le multi-tenant base de données, la forensics de session et un plan d’admin dans un seul binaire.
**Les autres** pour 100+ fournisseurs (LiteLLM), Node + base embarquée (OmniRoute), SaaS zéro-ops (Portkey), ou API GW générale + LLM (Kong).

Voir [comparaison détaillée](docs/comparison.md).

---

## Feuille de route

**Maintenant (v2.5.x)** : compatibilité protocoles · RLS · routage deux couches + sticky · compression / replay / cycle de vie · admin Vue · Docker Compose

**Ensuite (3–6 mois)** : passer le routage cost/quality de shadow à option par défaut · Helm prod · modèles Grafana · replay de décisions

**Exploration** : passerelle d’outils MCP (T3 2026) · A2A · Operator Kubernetes

Voir [ROADMAP.md](ROADMAP.md).

---

## 📚 Documentation

| Catégorie | Documents |
|-----------|-----------|
| Démarrage / architecture / API | [getting-started](docs/getting-started.md) · [architecture](docs/architecture.md) · [API](docs/03-design/01-architecture/architecture/API.md) |
| Comparaison / vue d’ensemble | [comparison](docs/comparison.md) · [PROJECT_OVERVIEW](docs/PROJECT_OVERVIEW.md) · [INDEX](docs/INDEX.md) |
| Guide session | [session-management](docs/04-implementation/deliverables/user-guide/session-management.md) |
| Dual-repo / sécu / légal | [REPO-MIRROR-POLICY](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md) · [SECURITY.md](SECURITY.md) · [disguise-compliance](docs/02-resources/compliance/legal/disguise-compliance.md) |
| Recherche | [A2A](docs/03-design/01-architecture/architecture/a2a-spec-2027.md) · [Armor/SDP](docs/03-design/01-architecture/architecture/armor-sdp-feasibility.md) |

---

## 🔀 Stratégie dual-dépôt

| Remote | URL | Usage |
|--------|-----|-------|
| `codeup` (origin) | `https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git` | Développement quotidien |
| `github` | `git@github.com:halfking/ai-native-gateway-core.git` | Miroir public |

```bash
git push              # → codeup
git push github       # → github (scan strict 49 règles, blocage si hit)
```

Voir la [politique miroir](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md).

## 🤝 Contribuer

Voir [CONTRIBUTING.md](CONTRIBUTING.md). Les changements multi-tenant doivent passer `lint-tenant-scope-llmgw` / `lint-pg-rls` / `lint-otel-tenant`.

## 🔐 Sécurité

Vulnérabilités : [SECURITY.md](SECURITY.md). Scan du miroir public : politique dual-repo. Liste blanche de déguisement : [disguise-compliance](docs/02-resources/compliance/legal/disguise-compliance.md).

## Licence

[Apache License 2.0](LICENSE). Conservez les mentions de copyright et [NOTICE](NOTICE) lors de la redistribution.

---

Construit avec ❤️ par la communauté AI Native Gateway
