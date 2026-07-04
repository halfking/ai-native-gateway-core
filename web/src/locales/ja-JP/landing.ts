// landing.ts — ランディングページコピー（ゲストホームページ）。
//
// 2026-07-05: 現在の official-deploy プロジェクトの実際の内容に更新（LandingView.vue に一致）。
export default {
  kicker: 'コアオープンソース · 中国ローカライゼーション · エンタープライズグレード',
  title: 'LLM Gateway — グローバル市場向けオープンソース AI ゲートウェイ',
  subtitle: 'コアオープンソースと深い中国ローカライゼーションを兼ね備えた唯一の AI ゲートウェイ。エンタープライズガバナンス、グローバル LLM アクセス、コンプライアンスとデータ主権 — すべてコアオープンソース。',
  featuresTitle: 'コア機能',
  featuresSubtitle: 'アクセスから運用までの重要な側面をカバー',
  heroPoints: [
    'コアオープンソース · Apache 2.0',
    '中国ローカライゼーション · 等保 2.0',
    'エンタープライズ AI ゲートウェイ',
    'Vibe Coding ガバナンス',
    'AI セッション資産管理',
    'データセキュリティシールド',
  ],
  features: {
    smartRouting: {
      title: 'スマートルーティングと認証情報プール',
      description: 'テナント、モデル、タスクタイプによる自動ルーティング；マルチ認証情報フィンガープリントプール + アダプティブプローブ、秒単位のフェイルオーバー、ほぼゼロの禁止率。',
    },
    safety: {
      title: 'コールセキュリティシールド',
      description: 'LLM-as-judge プロンプトインジェクション検出（v1 観測可能モード）+ 機密データマスキング計画、エンタープライズコンプライアンス防御。',
      badge: 'beta',
    },
    cache: {
      title: 'キャッシュアライメントとコスト削減',
      description: 'プロンプトプレフィックス安定化 + セマンティックキャッシング、KV Cache ヒット率の最大化、トークン計算オーバーヘッドの削減。',
    },
    agent: {
      title: 'Agent と MCP ゲートウェイ',
      description: 'Agent レジストリ、A2A プロトコル、MCP ツールホスティングとプロトコル変換 — LLM プロキシから Agent オーケストレーションゲートウェイへのアップグレード。',
      badge: '近日公開',
    },
    observability: {
      title: 'フルチェーン観測可能性',
      description: 'リクエストログ、ルーティング決定監査、OTel トレーシング、SIEM/CEF イベントエクスポート、等保 2.0 と GDPR 対応。',
    },
    billing: {
      title: 'MaaS 課金システム',
      description: 'プラン + クレジット + 三層ウォレット（サブスクリプション / クレジット / チャージ）、テナントセルフサービス向けの完全な商業化ループ。',
    },
    multiProtocol: {
      title: 'マルチプロトコル互換性',
      description: 'OpenAI Chat / Anthropic Messages / Responses 三つのインバウンドプロトコルを統一、オープンソースと商用モデルへのシームレスなアクセス。',
    },
    multiTenant: {
      title: 'マルチテナント分離',
      description: 'PostgreSQL RLS 行レベルセキュリティ + 43 ラウンド監査 L1=0、テナント間のデータ漏洩ゼロ、テナントごとの独立したポリシーとクォータ。',
    },
  },
  advantagesTitle: 'LLM Gateway を選ぶ理由',
  advantagesSubtitle: '中国ビジネスニーズを持つグローバル企業向け',
  advantages: {
    local: {
      title: '深い中国ローカライゼーション',
      description: '完全な中国語インターフェース、国内オープンソース LLM 優先アクセス、Alipay/WeChat Pay 統合、等保 2.0 コンプライアンステンプレート、国内クラウドインフラストラクチャ対応',
    },
    private: {
      title: 'プライベートデプロイメント',
      description: '完全なプライベートデプロイメント、データは企業内に留まる、k3s + Docker デュアルフォーム、外部依存関係ゼロ',
    },
    antiBan: {
      title: 'アンチバンシステム',
      description: '50+ UA ローテーション + utls TLS フィンガープリントプール + 11 ブラウザプロファイル + 5 分自動ローテーション',
    },
    perf: {
      title: 'Go 高性能データプレーン',
      description: 'ネイティブ Go 実装、40MB 軽量イメージ、200 同時接続 P99 < 500ms、SSE ストリーミング安定リレー',
    },
  },
  footer: 'LLM Gateway · llmgateway.internal.example.com · コアオープンソース · 中国ローカライゼーション · プライベートデプロイメント',
  ariaPoints: '主なハイライト',
  roadmap: {
    title: '製品ロードマップ',
    subtitle: 'LLM データプレーンからエンタープライズ Agent ゲートウェイへ、継続的な構築',
    v31: {
      phase: 'v3.1 · 2026 Q3',
      title: 'API Hub アセットセンター + MCP ツールホスティング',
      description: 'LLM エンドポイント、MCP サービス、Agent の統一登録、開発者セルフサービスによる発見と再利用。',
    },
    v32: {
      phase: 'v3.2 · 2026 Q4',
      title: 'セキュリティシールド GA + SIEM 統合 + SpecBoost',
      description: 'プロンプトインジェクションブロッキング、機密データマスキング、API 記述インテリジェントエンリッチメントによる Function Calling 精度向上。',
    },
    v40: {
      phase: 'v4.0 · 2027 Q1',
      title: 'Agent レジストリ + A2A プロトコルゲートウェイ',
      description: 'クロス Agent タスク委任とオーケストレーション、OpenClaw とビジネス Agent への統一アクセス。',
    },
    v50: {
      phase: 'v5.0 · 2027 Q3',
      title: '業界ソリューション GA',
      description: 'カスタマーサービス、HR、営業、物流の 4 つの業界テンプレート、すぐに使える Agent ソリューション。',
    },
  },
}
