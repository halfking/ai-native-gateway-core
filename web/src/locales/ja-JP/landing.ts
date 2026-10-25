// landing.ts — Landing page copy (guest homepage).
//
// 2026-10-25 rewrite (impeccable craft-floor / STYLE pass).
// Rebuilt from the zh-CN / en-US samples. The previous draft was still the
// English marketing text: a three-item string ("Core Open Source ·
// Enterprise-Grade · Private Deployment"), adjectives standing in for facts,
// em dashes doing the job of punctuation, and every description a tidy
// "X + Y + Z". Rules live in DESIGN.md §7 "Voice" at the repo root.
//
// Fact discipline: this version claims only what the product actually does
// (Apache 2.0 / 40 MB image / 200 concurrent with P99 under 500 ms / RLS
// row-level security / 43 audit rounds with zero L1 findings / k3s and Docker
// / three inbound protocols / 15-day trial). No new capability is implied.
// Written as natural Japanese, not Chinese-derived kanji phrasing.
export default {
  // Rendered only in the PublicPageShell footer (brand name + this). It is no
  // longer used as a small label above the heading (craft-floor bans kickers).
  kicker: 'Apache 2.0 のオープンソースコア',
  brandTitle: 'AI-Native 組織向けコアゲートウェイ',
  // already says that. It says what the thing does instead.
  title: '会社が使うすべての LLM 呼び出しを、把握でき、数えられ、止められる入口にまとめる',
  subtitle:
    'OpenAI、Anthropic、Responses の 3 つのプロトコルを同じゲートウェイで受け付けます。テナントとモデルで経路を選び、課金、監査、レート制限もここにあります。自社のマシンで動かすので、トラフィックは外に出ません。',
  featuresTitle: 'できること',
  featuresSubtitle: '受付、経路選択、課金、監査を、まとめて同じ入口で',
  // The six capability facts on the right-hand rail. Every one is checkable.
  heroPoints: [
    'コアは Apache 2.0 で公開',
    'データプレーンは Go、イメージ 40 MB',
    '同時 200 リクエストで P99 500 ms 未満',
    '3 つの入力プロトコルを 1 つに統一',
    'PostgreSQL RLS による行レベル分離',
    'k3s か Docker、自社のハードウェアで',
  ],
  features: {
    smartRouting: {
      title: 'テナントとタスクで経路を選ぶ',
      description:
        'テナント、モデル、タスク種別から経路を自動で選びます。上流が壊れたらプローブが検知して次の経路へ切り替わり、資格情報のフィンガープリントプールがローテーションを担当します。',
    },
    safety: {
      title: 'プロンプトインジェクションと機密データ',
      description:
        'LLM-as-judge がプロンプトインジェクションを検知します（v1 は観測のみで、ブロックしません）。機密フィールドはルールでマスクします。',
      badge: 'beta',
    },
    cache: {
      title: 'キャッシュとコスト',
      description:
        'プレフィックスの固定とセマンティックキャッシュで KV Cache のヒット率が上がり、token にかかる費用が減ります。',
    },
    agent: {
      title: 'Agent と MCP の接続',
      description:
        'Agent レジストリ、A2A プロトコル、MCP ツールのホスティングとプロトコル変換。LLM の中継から Agent のオーケストレーションへ移れます。',
      badge: '近日公開',
    },
    observability: {
      title: 'リクエストは 1 件残す',
      description:
        'リクエストログ、経路決定の監査、OTel トレーシング、SIEM/CEF イベント出力。障害が起きたら経緯まで追えます。',
    },
    billing: {
      title: '使用量と請求が合う',
      description:
        'プラン、クレジット、3 つのプールを持つウォレット（契約、クレジット、チャージ）。テナントが自分で使用量を調べられるので、管理者に聞く必要はありません。',
    },
    multiProtocol: {
      title: '3 つのプロトコル、1 つの入口',
      description:
        'OpenAI Chat、Anthropic Messages、Responses を入力側で統一します。クライアント側はそのまま、ゲートウェイも変わりません。',
    },
    multiTenant: {
      title: 'テナントが混ざらない',
      description:
        'PostgreSQL RLS の行レベルセキュリティ、43 回の監査で L1 指摘 0 件。テナントごとに独立したポリシーとクォータを設定します。',
    },
  },
  advantagesTitle: '自社で動かす理由',
  advantagesSubtitle: 'データは自社に置き、請求も自分で読める状態にする',
  advantages: {
    openSource: {
      title: 'コアは公開',
      description:
        'データプレーン、コントロールプレーン、認証、経路制御まですべて Apache 2.0 で公開しています。改造でき、監査もできます。',
    },
    private: {
      title: '自社ハードウェアで運用',
      description: '自社マシンに k3s か Docker でインストールします。外部サービスに依存しません。',
    },
    antiBan: {
      title: 'BAN されても止まらない',
      description:
        '複数の資格情報のローテーションと能動的なプローブ。1 枚が BAN されても次の資格情報が引き継ぎます。',
    },
    perf: {
      title: 'Go 製のデータプレーン',
      description:
        'ネイティブ Go、イメージ 40 MB、同時 200 でも P99 は 500 ms 未満、SSE のストリーミング中継も安定しています。',
    },
  },
  footer: 'コアは Apache 2.0 で公開し、自社のマシンで動かします。',
  roadmap: {
    title: 'これからの流れ',
    subtitle: 'LLM データプレーンから、企業向け Agent ゲートウェイへ',
    v31: {
      phase: 'v3.1',
      title: 'API Hub アセットセンター + MCP ツールホスティング',
      description:
        'LLM エンドポイント、MCP サービス、Agent を 1 か所に登録し、開発者が自分で探して再利用できます。',
    },
    v32: {
      phase: 'v3.2',
      title: '注入のブロック対応 + SIEM 連携 + SpecBoost',
      description:
        'プロンプトインジェクションの観測からブロックへ移行します。機密データのマスク、API 記述の自動補完で Function Calling の精度を上げます。',
    },
    v40: {
      phase: 'v4.0',
      title: 'Agent レジストリ + A2A プロトコルゲートウェイ',
      description: 'Agent 同士がタスクを委任し合えます。OpenClaw も自社 Agent も同じ入口から接続します。',
    },
    v50: {
      phase: 'v5.0',
      title: '業界向けソリューションの正式版',
      description: '客服、人事、営業、物流の 4 つのテンプレート。入れるだけで動きます。',
    },
  },
  // 2026-07-21: guest-nav aria + top download/activation links (App.vue L197-203).
  guestNavAria: 'ナビゲーション',
  navDownload: 'ダウンロード',
  navSetup: 'インストールと激活',
  navActivate: 'オンライン激活',
  navLicense: 'ライセンス状態',
  navAgreement: '利用規約',
  navSupport: 'サポート',
  // 2026-07-21: landing primary CTAs (LandingView.vue L109-119).
  ctaLogin: 'ログイン',
  ctaDownload: 'パッケージをダウンロード',
  ctaActivate: 'ライセンスを激活',
  ctaAgreement: '利用規約を見る',
}
