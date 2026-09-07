# AI Native Gateway（AI ネイティブゲートウェイ）

> AI ネイティブな LLM ゲートウェイ：単なるプロキシではなく、エンタープライズ AI トラフィックのための**管理プレーン & セッションガバナンス基盤**。

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8.svg)](https://golang.org)
[![Version](https://img.shields.io/badge/version-2.5.x-green.svg)](CHANGELOG.md)

[English](README.md) | [简体中文](README.zh-CN.md) | **日本語**

[クイックスタート](#-クイックスタート) • [コアバリュー](#-4つのコアバリュー) • [セッションガバナンス](#-セッションガバナンス) • [機能プレビュー](#-製品機能プレビュー) • [差別化・比較](#-差別化ポジショニングと競合比較) • [アーキテクチャ](docs/architecture.md) • [ロードマップ](ROADMAP.md)

---

## AI Native Gateway とは？

AI Native Gateway は、AI エージェントと「バイブコーディング」の時代のために作られた**オープンソース・自己ホスト型の AI ネイティブ LLM ゲートウェイ**です。単にリクエストを転送するだけでなく、組織を流れるすべてのトークンを**分類・ルーティング・ガバナンス・監査・課金**します。

- **プロトコル正規化**：OpenAI / Anthropic / Gemini / Responses API 互換 —— 1 つのエンドポイントですべてのモデルに接続
- **2 層インテリジェントルーティング**：L1 でモデルを選択（タスク自動分類 → 6 項目スコアリング → プロファイル固定）、L2 でクレデンシャルを選択（ティアフォールバック → 課金ラウンド → P2C スコアリング → 実行 / サーキットブレーク）
- **セッションガバナンス**：スティッキーセッション、全文セッションリプレイ、自動プロンプト圧縮、4 段階データライフサイクル —— 「リクエスト」ではなく「会話」を第一級の管理対象として扱う
- **マルチテナント**：PostgreSQL RLS によるデータベースレベル分離（38 以上のテーブル）、テナント別クォータ・計量・MaaS 課金に対応
- **クレデンシャル管理**：複数クレデンシャルプール + フィンガープリント偽装 + 適応型プロービング + 自動サーキットブレーク
- **オブザーバビリティ**：リアルタイムリクエストストリーム、ルーティング全景（ヒートマップ + Sankey）、コスト追跡、OTel + Prometheus
- **プライバシー**：100% プライベートデプロイ —— すべてのデータが自社インフラ内に留まる

技術スタックは **Go + PostgreSQL + Redis**。k3s 本番環境で稼働中（同一 PostgreSQL スキーマを共有する 2 インスタンス構成）。AI インフラを完全にコントロールしたい組織のために設計されています。

---

## ✨ 4 つのコアバリュー

| バリュー | お客様が得られるもの |
|----------|----------------------|
| **セキュア** | AI Guardrails · DLP · インラインインターセプション · バイブコーディングガバナンス · SIEM/SOAR 連携 |
| **安定** | マルチクラウドオーケストレーション · サーキットブレーカー（自動回復）· 99.9% SLA 目標 |
| **低コスト** | セマンティックキャッシュ · オートルーティング（cost/quality ポリシー）· トークン計量 · プロンプト圧縮 |
| **エンタープライズ統合** | MCP ツールゲートウェイ（ロードマップ）· API Hub 資産センター（ロードマップ）· フルチェーン監査 · SIEM/SOAR 連携 |

## 🏗️ 3 つの製品ピラー

| Control（管理） | Govern（ガバナンス） | Secure（セキュリティ） |
|------------------|----------------------|------------------------|
| ✅ トークン使用量計測 | 🔨 API Hub 資産センター | 🔨 Model Armor |
| ✅ インテリジェントルーティング + スティッキーセッション | 🔨 自動ディスカバリー | 🔨 機密データ保護（SDP） |
| ✅ セマンティックキャッシュ + Funnel | 🔨 SpecBoost スマートエンリッチメント | 🔨 対抗型プロンプト防御 |
| ✅ フルチェーン監査 + OTel | ✅ マルチテナント RLS（L1=0） | ✅ SIEM/SOAR 連携 |
| ✅ MaaS 課金 | | |

✅ = リリース済み &nbsp;·&nbsp; 🔨 = ロードマップ

## 🎯 現在の能力

| レイヤー | 提供内容 |
|----------|----------|
| **プロトコル** | OpenAI / Anthropic / Gemini / Responses 互換 + SSE ストリーミング中継（増分整合性チェック付き） |
| **ルーティング** | 2 層ルーティング（モデル → クレデンシャル）+ スティッキーセッション + 自動ルーティング（cost/quality ポリシー） |
| **マルチテナント** | アイデンティティトンネル（virtual IP/MAC/ClientID）+ クレデンシャルプール + 38 以上のテーブルに RLS |
| **トラフィックガバナンス** | トークンレート制限（TPM/RPM）+ セマンティックキャッシュ + プロンプト圧縮 + スライディングウィンドウ |
| **監査** | フルチェーン監査 + DLQ + ディスクフォールバック + OTel + Prometheus |
| **クレデンシャル** | マルチクレデンシャル + フィンガープリントプール + 適応型プロービング + 手動無効化 |
| **デプロイ** | 2 インスタンス（Docker + k3s NodePort）、同一 PG スキーマを共有 |

詳細は[アーキテクチャドキュメント](docs/architecture.md)を参照してください。

---

## 🧠 セッションガバナンス

多くのゲートウェイは各リクエストを孤立したイベントとして扱います。AI Native Gateway は**セッション**をガバナンスの基本単位として扱います —— コーディングエージェントと長時間稼働アシスタントの時代には、1 回の「リクエスト」では物事の全貌を捉えられないからです。

- **スティッキーセッションバインディング**：セッションをクレデンシャルに固定し、モデル切り替えやフェイルオーバー後も会話コンテキストを維持 —— タスク途中でコンテキストが静かに失われることがありません
- **セッションレベルの監査とリプレイ**：system prompt とレスポンス全文を保存し、セッション単位で再生可能（本番環境で 13,000 以上のセッションを保有）。タスク種別・クライアントモデル・出力モデル・プロバイダ・トークン・レイテンシ・終了理由を網羅し、Key / テナント / 期間でスライスしてトラブルシューティングとコンプライアンス監査に活用
- **自動プロンプト圧縮**：リクエストがプロバイダの実際のコンテキストウィンドウに近づくと（約 80% で発火）、ディスパッチ前にメッセージレベルの圧縮を実行 —— 長いエージェントセッションが小さいウィンドウにも収まります。圧縮イベント（戦略・しきい値・圧縮前後のサイズ）はすべてリクエストに記録されます
- **セッションメタデータインテリジェンス**：タスク種別の自動タグ付け（10 種類のワークタイプ）、プロジェクト帰属、タイトル抽出により、生のトラフィックを検索可能なナレッジに変換
- **アイデンティティトンネル**：エージェントトラフィックを virtual IP/MAC/ClientID でテナントに帰属させ —— 多数のエージェントが同一出口を共有してもマルチテナント分離は維持されます
- **4 段階データライフサイクル**：ホット（0〜7 日）/ ウォーム（7〜30 日）/ コールド（30〜90 日）/ 期限切れ（90 日超）の自動階層化。アーカイブプレビューにより「実行後に何件動くか」を事前表示 —— 誤削除を防止

---

## 🎛️ 製品機能プレビュー

以下の機能モジュールはすべてリリース済みで、k3s 本番環境にデプロイされています。スクリーンショットは実機ローカルデプロイ（1728×1050、全データ読み込み後）から撮影。

### ダッシュボード — リアルタイムリクエストストリーム

![リアルタイムリクエストストリーム](docs/assets/screenshots/dashboard-request-stream.png)
*処理キューごとにグループ化されたライブリクエストストリーム。ディスパッチチェーン統計（処理中、p50/p95 レイテンシ、ノード可用性）とモデル別ノードヘルス*

### ルーティング全景 — 2 層ルーティングの完全可視化

![ルーティング全景](docs/assets/screenshots/routing-panorama.png)
*L1 モデル選択（タスク分類 → 6 項目スコアリング → プロファイル固定）+ L2 クレデンシャル選択（モデル解決 → ティアフォールバック → 課金ラウンド → P2C スコアリング → 実行 / サーキットブレーク）。タスク×モデルのヒートマップが「どのタスクにどのモデル」に答え、Sankey フローが 14,000 以上のライブリクエストの最終的な行き先（タスク → モデル → プロバイダ）を表示*

### クレデンシャルモニター — マルチソース × マルチクレデンシャルの健全性

![クレデンシャルモニター](docs/assets/screenshots/credential-monitor.png)
*2 次元の可用性マトリクス（本番環境で 19 クレデンシャル × 18 モデル）で、どのクレデンシャルがどのモデルを壊しているか一目で把握。クレデンシャルごとの P95 レイテンシ、直近 1 時間のスライディングウィンドウ成功率、同時実行スロット使用率。フィンガープリントプール（50+ User-Agent · 35 種 Accept-Language · 11 種 uTLS プロファイル）+ 適応型プロービングが上流のリスクコントロールを自動回避 —— 障害はブレーカーが遮断し、人の介入は不要*

### ワークタイプ設定 — タスク自動分類

![ワークタイプ](docs/assets/screenshots/work-types.png)
*タスク自動分類（10 ワークタイプ）と 24 時間分布、上位モデル、ルーティング決定統計 —— 管理画面でワークタイプごとに設定可能*

### フリーリソースプール

![フリープール](docs/assets/screenshots/free-pool.png)
*無料モデルのリソースプール：持ち込みキー、プロバイダテンプレート（Groq、Google AI Studio、OpenRouter、SiliconFlow、Zhipu）、モデル別ルーティング優先度*

### リクエストセッション詳細 — フルチェーンフォレンジック

![リクエスト詳細](docs/assets/screenshots/request-detail.png)
*リクエストの完全な検査：Q&A リプレイ、ディスパッチウォーターフォール、ルーティング & リトライ履歴、トレース、圧縮/マスキング記録（マスクされた API キー `sk-****` に注目）、トークン & キャッシュ統計*

---

## 🚀 クイックスタート

### 方法 A：Docker Compose（推奨、10 分以内）

```bash
# 1. クローン
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git ai-native-gateway
cd ai-native-gateway
# （GitHub ミラー：git clone https://github.com/halfking/SI-LLM-Gateway.git）

# 2. セキュアなキーを生成
cp .env.quickstart.example .env
# .env を編集し、安全なランダム値を設定（生成コマンドはファイル内コメントを参照）

# 3. スタックを起動（PostgreSQL + Redis + Gateway）
docker compose -f docker-compose.quickstart.yml up -d

# 4. ヘルスチェック
curl http://localhost:8781/healthz
# {"status":"ok"}

# 5. 内蔵管理 UI を開く
open http://localhost:8781/admin
```

### 方法 B：ソースからビルド

```bash
go build -o gateway ./cmd/gateway
./gateway --config=configs/local.yaml
curl http://localhost:8781/healthz   # → 200 OK
```

詳細は[スタートガイド](docs/getting-started.md)を参照してください。

Git フックのインストール（開発者向け、任意）：

```bash
./scripts/install-githooks.sh --pre-commit  # pre-commit: go vet + SQL lint + migration 番号検証
./scripts/install-githooks.sh               # pre-push: github への push 時に機密情報スキャン
```

---

## デプロイモード

AI Native Gateway は**完全な本番スタック**と**シングルマシンの最小構成デプロイ**の両方をサポートします：

| モード | 説明 | 状態 |
|--------|------|------|
| **Docker Compose** | PostgreSQL/Redis 込みのクイックスタート | ✅ 評価に推奨 |
| **バイナリ + systemd** | Linux ホストへの本番デプロイ | ✅ インストーラ提供 |
| **Kubernetes** | Deployment + ConfigMap + Service マニフェスト | ⚠️ テストグレード（内部の本番は k3s で稼働） |

**本番スタック構成**：Gateway + PostgreSQL 14+（永続状態、RLS）+ Redis 7+（ホット状態、レート制限）+ 内蔵管理 UI、オプションで Prometheus/Grafana 監視。

クイックスタートのスタックは本番と**同一バイナリ・同一スキーマ**です —— 本番移行はマネージド PostgreSQL/Redis への向け直しと TLS 追加のみで、設定セマンティクスの変更は不要です。内部の本番環境は**同一 PG スキーマを共有する 2 インスタンス（Docker ホスト + k3s NodePort）**で稼働しています。

**本番要件**：外部 PostgreSQL 14+ と Redis 7+、TLS 終端（リバースプロキシ）、シークレット管理、バックアップと監視。

詳細は[本番デプロイドキュメント](docs/06-deployment/)を参照してください。

---

## 📐 差別化ポジショニングと競合比較

### 汎用 AI ゲートウェイとの比較

| 項目 | 汎用 AI Gateway | AI Native Gateway |
|------|-----------------|-------------------|
| **デプロイ** | SaaS / On-Prem | 完全プライベート（k3s 本番実証済み） |
| **データコンプライアンス** | データが外部に出る | すべてのデータが社内に留まる |
| **課金** | 従量課金（USD） | プラン + クレジット + ブースターパック、Alipay 対応 |
| **上流モデル** | 一部の大手ベンダー中心 | 幅広いモデル + 中国国産モデル + ローカルモデル |
| **クレデンシャルフィンガープリントプール** | 基本的 | 50+ UA · 35 種 Accept-Language · 11 種 uTLS プロファイル |
| **セッションガバナンス** | リクエスト単位のログ | スティッキーセッション + 全文リプレイ + 圧縮 + 4 段階ライフサイクル |
| **MCP ツールゲートウェイ** | 一部 | 2026 Q3 にフル提供予定 |
| **中国語対応** | 限定的 | 完全中国語 UI + 国産モデル + Alipay 決済 |
| **マルチテナント監査** | 標準 | 38+ テーブル RLS · テナント監査 L1=0 |

### 有名な代替製品との比較

| 機能 | AI Native Gateway | LiteLLM | OmniRoute | Portkey | Kong AI |
|------|-------------------|---------|-----------|---------|---------|
| **デプロイ** | プライベート（自己ホスト） | SaaS + OSS | 自己ホスト（Node） | SaaS | OSS |
| **マルチテナント** | ネイティブ（PG RLS） | 基本的 | シングルノード指向 | フル（SaaS） | プラグイン経由 |
| **管理 UI** | 内蔵 Vue SPA | CLI | Web UI | SaaS UI | Kong Manager |
| **データレジデンシー** | 100% プライベート | モードによる | 100% プライベート | クラウド（SaaS） | 自己ホスト可 |
| **ライセンス** | Apache 2.0 | MIT | 上流を参照 | プロプライエタリ | Apache 2.0 |

**AI Native Gateway を選ぶべきケース**：

- 完全なデータレジデンシー管理（外部 SaaS 依存なし）が必要
- データベースレベルの深いマルチテナント分離が必要
- リクエストログではなく、セッションレベルのガバナンスとフォレンジックが必要
- 単一 Go バイナリに内蔵された管理 UI が必要
- 中国の SMB に適した MaaS 課金（プラン + クレジット + ブースターパック）が必要

**代替製品を選ぶべきケース**：

- 最大のプロバイダカバー範囲（100+ プロバイダ）→ LiteLLM
- Node.js 製の自己ホストゲートウェイ → OmniRoute
- ゼロ運用のマネージドサービス → Portkey
- 汎用 API ゲートウェイ + LLM → Kong

詳細は[詳細比較ドキュメント](docs/comparison.md)を参照してください。

---

## ロードマップ

**現在（v2.x）**：

- ✅ OpenAI / Anthropic / Gemini / Responses プロトコル対応
- ✅ PostgreSQL RLS によるマルチテナント分離
- ✅ スティッキーセッションによるインテリジェントルーティング
- ✅ セッションガバナンス：圧縮、リプレイ、ライフサイクル
- ✅ Vue.js 管理コンソール
- ✅ Docker Compose クイックスタート

**近い将来（3〜6 ヶ月）**：

- 🚧 cost/quality 感知ルーティングの強化
- 🚧 本番グレードの Kubernetes Helm Charts
- 🚧 Grafana ダッシュボードテンプレート
- 🚧 高度なオブザーバビリティ（セッションフォレンジック、ディシジョンリプレイ）

**探究中（6〜12 ヶ月以上）**：

- 🔬 MCP（Model Context Protocol）ゲートウェイ統合 —— 2026 Q3 フル提供目標
- 🔬 エージェント間通信（A2A）プロトコル対応
- 🔬 Kubernetes Operator（CRD ベースのデプロイ）

完全なロードマップは [ROADMAP.md](ROADMAP.md) を参照してください。

---

## 📚 ドキュメント

| カテゴリ | ドキュメント |
|----------|--------------|
| スタートガイド | [docs/getting-started.md](docs/getting-started.md) — 10 分でデプロイ |
| アーキテクチャ | [docs/architecture.md](docs/architecture.md) — システム設計とコンポーネント |
| API | [docs/03-design/01-architecture/architecture/API.md](docs/03-design/01-architecture/architecture/API.md) — データプレーンと管理 API 仕様 |
| 環境 | [docs/environment.md](docs/environment.md) — デプロイ環境と変数 |
| クイックリファレンス | [docs/QUICK_REFERENCE.md](docs/QUICK_REFERENCE.md) — よく使うコマンドとトラブルシューティング |
| 比較 | [docs/comparison.md](docs/comparison.md) — vs LiteLLM / OmniRoute / Portkey / Kong |
| プロジェクト概要 | [docs/PROJECT_OVERVIEW.md](docs/PROJECT_OVERVIEW.md) — 機能とモジュールマップ |
| ドキュメント索引 | [docs/INDEX.md](docs/INDEX.md) — 全ドキュメントナビゲーション |
| デュアルリポジトリ | [docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md) — codeup ⇄ GitHub ワークフロー |
| セキュリティ | [SECURITY.md](SECURITY.md) — 脆弱性報告 + スキャナの使い方 |
| 法務 | [docs/02-resources/compliance/legal/disguise-compliance.md](docs/02-resources/compliance/legal/disguise-compliance.md) — リクエスト偽装のコンプライアンスホワイトリスト |
| A2A 調査 | [docs/03-design/01-architecture/architecture/a2a-spec-2027.md](docs/03-design/01-architecture/architecture/a2a-spec-2027.md) — エージェント間通信プロトコル調査 |
| Armor/SDP | [docs/03-design/01-architecture/architecture/armor-sdp-feasibility.md](docs/03-design/01-architecture/architecture/armor-sdp-feasibility.md) — プロンプトインジェクション + SDP 実現可能性 |

---

## 🔀 デュアルリポジトリ戦略

| Remote | URL | 用途 |
|--------|-----|------|
| `codeup`（origin） | `https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git` | デフォルト（日常開発） |
| `github` | `git@github.com:halfking/SI-LLM-Gateway.git` | 公開ミラー（段階的リリース） |

```bash
git push              # → codeup（追加チェックなし）
git push github       # → github（厳格スキャン自動実行、検出時はブロック）
```

機密情報保護：`.githooks/pre-push` が github への push 時に `scripts/scan-secrets.sh` を厳格モード（49 ルール）で自動実行します。詳しくは[ミラーポリシー](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md)を参照。

---

## 🤝 コントリビューション

コントリビューションを歓迎します！開発環境の構築、コードスタイル、PR プロセスは [CONTRIBUTING.md](CONTRIBUTING.md) を参照してください。

マルチテナント関連の変更は、`lint-tenant-scope-llmgw` / `lint-pg-rls` / `lint-otel-tenant` の 3 つのリンターすべてに合格する必要があります。

## 🔐 セキュリティ

- 脆弱性報告：[SECURITY.md](SECURITY.md) を参照
- 公開ミラーの機密情報保護：[デュアルリポジトリポリシー](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md) を参照
- 偽装コンプライアンスホワイトリスト：[docs/02-resources/compliance/legal/disguise-compliance.md](docs/02-resources/compliance/legal/disguise-compliance.md) を参照

## ライセンス

[Apache License 2.0](LICENSE) でライセンスされています。再配布時は著作権表示と [NOTICE](NOTICE) ファイルを保持してください。

**商用利用**：Apache 2.0 は商用利用を許可します。ソースまたはバイナリでの再配布時は、著作権表示と NOTICE ファイルの保持が必要です。再配布を伴わない社内商用利用には、ライセンス遵守以上の追加表示は不要です。

## 謝辞

AI Native Gateway は以下のオープンソースプロジェクトのコンポーネントを利用しています：

- Go 標準ライブラリ（BSD-3-Clause）
- PostgreSQL ドライバ（MIT）
- Redis クライアント（BSD-2-Clause）
- Vue.js と Element Plus（MIT）
- 完全なリストは [NOTICE](NOTICE) を参照

---

AI Native Gateway コミュニティが ❤️ を込めて構築
