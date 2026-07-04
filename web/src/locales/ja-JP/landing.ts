// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// landing.ts — ランディングページの文案（未ログインのトップページ）。LandingView が ServiceLandingPage に渡す props と、
// LandingView テンプレート内の「ロードマップ」セクションを含みます。
//
// camelCase のネストされたオブジェクトを使用し、vue-i18n の t() 補間と t('landing.features.X.title', {...}) の置換に対応します。
export default {
  kicker: 'AI-Native · Enterprise Governance',
  title: 'AI-Native 組織コアゲートウェイ',
  subtitle: [
    'AI を組織のネイティブ能力に。',
    'オープンコア + エンタープライズ強化 + Vibe Coding ガバナンス + セッション資産化。',
  ],
  featuresTitle: 'コア機能',
  featuresSubtitle: '接続から運用までの重要なステップをカバー',
  heroPoints: ['スマートルーティング', '呼び出しのセキュリティ', 'キャッシュによるコスト削減', 'Agent 対応', 'エンドツーエンド監査', 'MaaS 課金'],
  features: {
    smartRouting: {
      title: 'スマートルーティングと認証情報プール',
      description:
        'テナント、モデル、タスク種別ごとに自動選択。複数の認証情報指紋プール + 適応的検出により、障害を秒単位で切り替え、垢バン率を限りなくゼロに近づけます。',
    },
    safety: {
      title: '呼び出しセーフティシールド',
      description:
        'LLM-as-judge によるプロンプトインジェクション検出（v1 可観測モード）+ 機微データの脱敏化計画。エンタープライズコンプライアンスの防衛線。',
      badge: 'beta',
    },
    cache: {
      title: 'キャッシュ整合とコスト削減',
      description: 'プロンプトプレフィックス安定化 + セマンティックキャッシュにより、KV Cache のヒット率を最大化し、トークン消費を削減します。',
    },
    agent: {
      title: 'Agent と MCP ゲートウェイ',
      description: 'Agent 登録センター、A2A プロトコル、MCP ツールホスティングとプロトコル変換 — LLM プロキシからエージェントオーケストレーションの入口へアップグレード。',
      badge: '近日公開',
    },
    observability: {
      title: 'エンドツーエンドの可観測性',
      description: 'リクエストログ、ルーティング判断監査、OTel 分散トレーシング、SIEM/CEF イベントエクスポート。中国等保 2.0 と GDPR に対応。',
    },
    billing: {
      title: 'MaaS 課金システム',
      description: 'サブスクリプション + クレジット + 三つのウォレット（サブスク/credit/チャージ）で、テナント向けの完全な商用クローズドループ。',
    },
    multiProtocol: {
      title: 'マルチプロトコル対応',
      description: 'OpenAI Chat / Anthropic Messages / Responses の3つの入力方向を一元化し、国内および海外モデルをシームレスに統合。',
    },
    multiTenant: {
      title: 'マルチテナント分離',
      description: 'PostgreSQL RLS による行レベルセキュリティ + 43 ラウンドの監査 L1=0 で、テナント間のデータ漏洩ゼロを実現。各テナントごとに独立したポリシーとクォータ。',
    },
  },
  advantagesTitle: '差別化された優位性',
  advantagesSubtitle: '海外ベンダーが提供できない能力',
  advantages: {
    local: { title: '中国ローカライズ', description: '完全な中国語インターフェース、国内モデル優先、Alipay/WeChat Pay 統合、等保コンプライアンステンプレート' },
    private: { title: 'プライベートデプロイ', description: '完全なプライベートデプロイでデータは企業外に出ません。k3s + Docker 両対応で外部依存ゼロ' },
    antiBan: { title: '垢バン対策', description: '50+ UA ローテーション + utls TLS 指紋プール + 11種類のブラウザプロファイル + 5分ごとの自動切替' },
    perf: { title: 'Go 高性能データプレーン', description: 'ネイティブ Go 実装、40MB 軽量イメージ、200 並行で P99 < 500ms、SSE ストリーミングを安定中継' },
  },
  footer: 'Kaixuan LLM Gateway · [GATEWAY_DOMAIN] · プライベートデプロイ · 中国ローカライズ',
  ariaPoints: '主な特徴',
  roadmap: {
    title: '製品ロードマップ',
    subtitle: 'LLM データプレーンからエンタープライズ Agent ゲートウェイへ',
    v31: {
      phase: 'v3.1 · 2026 Q3',
      title: 'API Hub アセットセンター + MCP ツールホスティング',
      description: 'LLM エンドポイント、MCP サービス、Agent を統一登録し、開発者がセルフサービスで利用可能に。',
    },
    v32: {
      phase: 'v3.2 · 2026 Q4',
      title: 'セーフティシールド GA + SIEM 連携 + SpecBoost',
      description: 'プロンプトインジェクション防御、機微データの脱敏化、API 説明の自動拡充による Function Calling 精度向上。',
    },
    v40: {
      phase: 'v4.0 · 2027 Q1',
      title: 'Agent 登録センター + A2A プロトコルゲートウェイ',
      description: 'エージェント間のタスク委譲とオーケストレーション。OpenClaw と業務 Agent を統一接続。',
    },
    v50: {
      phase: 'v5.0 · 2027 Q3',
      title: '業界ソリューション GA',
      description: 'カスタマーサポート、人事、セールス、物流の4業界テンプレート。すぐに使えるエージェントソリューション。',
    },
  },
}
