// Auto-translated draft (zh-TW/ja-JP) · 2026-07-02 · please review
// landing.ts — 落地頁文案（未登入首頁）。對應 LandingView 傳給 ServiceLandingPage 的 props，
// 以及 LandingView 範本內自帶的"路線圖"區塊。
//
// 使用 camelCase 巢狀物件，便於 vue-i18n 的 t() 插值與 t('landing.features.X.title', {...}) 替換。
export default {
  kicker: 'AI-Native · Enterprise Governance',
  title: 'AI-Native 組織核心閘道',
  subtitle: [
    '讓 AI 成為組織的原生能力。',
    '核心開源 + 企業增強 + Vibe Coding 治理 + 會話資產化。',
  ],
  featuresTitle: '核心能力',
  featuresSubtitle: '覆蓋從接入到營運的關鍵環節',
  heroPoints: ['智慧路由', '呼叫安全', '快取降本', 'Agent 就緒', '全鏈路稽核', 'MaaS 計費'],
  features: {
    smartRouting: {
      title: '智慧路由與憑證池',
      description:
        '依租戶、模型與任務類型自動選路；多憑證指紋池 + 自適應探測，故障秒級切換、封號率趨近於零。',
    },
    safety: {
      title: '呼叫安全護盾',
      description:
        'LLM-as-judge 提示詞注入偵測（v1 可觀測模式）+ 敏感資料脫敏規劃，企業級合規防線。',
      badge: 'beta',
    },
    cache: {
      title: '快取對齊與降本',
      description: 'Prompt 前綴穩定化 + 語意快取，最大化 KV Cache 命中率，降低 Token 算力開銷。',
    },
    agent: {
      title: 'Agent 與 MCP 閘道',
      description: 'Agent 註冊中心、A2A 協定、MCP 工具託管與協定轉換——從 LLM 代理升級為智慧體編排入口。',
      badge: '即將上線',
    },
    observability: {
      title: '全鏈路可觀測',
      description: '請求日誌、路由決策稽核、OTel 鏈路追蹤、SIEM/CEF 事件匯出，等保 2.0 與 GDPR 就緒。',
    },
    billing: {
      title: 'MaaS 計費體系',
      description: '套餐 + 積分 + 三池錢包（訂閱 / 信用 / 儲值），面向租戶自助的完整商業化閉環。',
    },
    multiProtocol: {
      title: '多協定相容',
      description: 'OpenAI Chat / Anthropic Messages / Responses 三套入向統一歸一，國產與海外模型無縫接入。',
    },
    multiTenant: {
      title: '多租戶隔離',
      description: 'PostgreSQL RLS 列級安全 + 43 輪稽核 L1=0，租戶間資料零洩漏，每租戶獨立策略與配額。',
    },
  },
  advantagesTitle: '差異化優勢',
  advantagesSubtitle: '海外廠商給不了的能力',
  advantages: {
    local: { title: '中國本地化', description: '全中文介面、國產模型優先、支付寶/微信支付接入、等保合規模板' },
    private: { title: '私有化部署', description: '完全私有部署，資料不出企業，k3s + Docker 雙形態，零外部依賴' },
    antiBan: { title: '抗封號體系', description: '50+ UA 輪換 + utls TLS 指紋池 + 11 瀏覽器 profile + 5 分鐘自動輪換' },
    perf: { title: 'Go 高效能資料面', description: '原生 Go 實作，40MB 輕量映像，200 並行 P99 < 500ms，SSE 串流穩定中繼' },
  },
  footer: '開軒 LLM Gateway · [GATEWAY_DOMAIN] · 私有部署 · 中國本地化',
  ariaPoints: '核心亮點',
  roadmap: {
    title: '產品演進路線',
    subtitle: '從 LLM 資料面到企業 Agent 閘道，持續建置',
    v31: {
      phase: 'v3.1 · 2026 Q3',
      title: 'API Hub 資產中心 + MCP 工具託管',
      description: '統一登記 LLM 端點、MCP 服務與 Agent，開發者自助發現與複用。',
    },
    v32: {
      phase: 'v3.2 · 2026 Q4',
      title: '安全護盾 GA + SIEM 對接 + SpecBoost',
      description: '提示詞注入攔截、敏感資料脫敏、API 描述智慧豐富提升 Function Calling 準確率。',
    },
    v40: {
      phase: 'v4.0 · 2027 Q1',
      title: 'Agent 註冊中心 + A2A 協定閘道',
      description: '跨智慧體任務委派與編排，OpenClaw 與業務 Agent 統一接入。',
    },
    v50: {
      phase: 'v5.0 · 2027 Q3',
      title: '行業方案 GA',
      description: '客服、人資、銷售、物流四大行業範本，開箱即用的智慧體方案。',
    },
  },
}
