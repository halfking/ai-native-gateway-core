// landing.ts — 落地頁文案（未登錄首頁）。
//
// 2026-07-05: 更新為中性化、全球化的產品定位。
export default {
  kicker: '核心開源 · 企業級 · 全球部署',
  title: 'LLM Gateway — 企業級開源 AI 網關',
  subtitle: '核心開源的企業級 AI 網關。統一治理、全球 LLM 接入、合規與資料主權 — 全部核心開源。',
  featuresTitle: '核心能力',
  featuresSubtitle: '覆蓋從接入到營運的關鍵環節',
  heroPoints: [
    '核心開源 · Apache 2.0',
    '企業級治理',
    '全球 LLM 接入',
    '資料安全防護',
    'AI 會話資產化',
    '私有化部署',
  ],
  features: {
    smartRouting: {
      title: '智慧路由與憑證池',
      description: '按租戶、模型與任務類型自動選路；多憑證指紋池 + 自適應探測，故障秒級切換、封號率趨零。',
    },
    safety: {
      title: '呼叫安全防護',
      description: 'LLM-as-judge 提示詞注入檢測（v1 可觀測模式）+ 敏感資料脫敏規劃，企業級合規防線。',
      badge: 'beta',
    },
    cache: {
      title: '快取對齊與降本',
      description: 'Prompt 前綴穩定化 + 語意快取，最大化 KV Cache 命中率，降低 Token 算力開銷。',
    },
    agent: {
      title: 'Agent 與 MCP 網關',
      description: 'Agent 註冊中心、A2A 協定、MCP 工具託管與協定轉換——從 LLM 代理升級為智慧體編排入口。',
      badge: '即將上線',
    },
    observability: {
      title: '全鏈路可觀測',
      description: '請求日誌、路由決策稽核、OTel 鏈路追蹤、SIEM/CEF 事件匯出，企業合規就緒。',
    },
    billing: {
      title: 'MaaS 計費體系',
      description: '套餐 + 積分 + 三池錢包（訂閱 / 信用 / 儲值），面向租戶自助的完整商業化閉環。',
    },
    multiProtocol: {
      title: '多協定相容',
      description: 'OpenAI Chat / Anthropic Messages / Responses 三套入向統一歸一，開源與商業模型無縫接入。',
    },
    multiTenant: {
      title: '多租戶隔離',
      description: 'PostgreSQL RLS 行級安全 + 43 輪稽核 L1=0，租戶間資料零洩漏，每租戶獨立策略與配額。',
    },
  },
  advantagesTitle: '為什麼選擇 LLM Gateway',
  advantagesSubtitle: '面向全球企業的企業級 AI 網關',
  advantages: {
    local: {
      title: '全球化部署',
      description: '多語言介面支援、全球主流 LLM 優先接入、靈活的支付整合、全球雲端基礎設施就緒',
    },
    private: {
      title: '私有化部署',
      description: '完全私有部署，資料不出企業，k3s + Docker 雙形態，零外部依賴',
    },
    antiBan: {
      title: '高可用體系',
      description: '多憑證輪換 + 智慧探測 + 自動故障切換，保障服務連續性',
    },
    perf: {
      title: 'Go 高效能資料面',
      description: '原生 Go 實作，40MB 輕量映像，200 並發 P99 < 500ms，SSE 串流穩定中繼',
    },
  },
  footer: 'LLM Gateway · 核心開源 · 企業級部署 · 全球化支援',
  ariaPoints: '核心亮點',
  roadmap: {
    title: '產品演進路線',
    subtitle: '從 LLM 資料面到企業 Agent 網關，持續建構',
    v31: {
      phase: 'v3.1',
      title: 'API Hub 資產中心 + MCP 工具託管',
      description: '統一登記 LLM 端點、MCP 服務與 Agent，開發者自助發現與複用。',
    },
    v32: {
      phase: 'v3.2',
      title: '安全防護 GA + SIEM 對接 + SpecBoost',
      description: '提示詞注入攔截、敏感資料脫敏、API 描述智慧富集提升 Function Calling 準確率。',
    },
    v40: {
      phase: 'v4.0',
      title: 'Agent 註冊中心 + A2A 協定網關',
      description: '跨智慧體任務委派與編排，OpenClaw 與業務 Agent 統一接入。',
    },
    v50: {
      phase: 'v5.0',
      title: '行業方案 GA',
      description: '客服、HR、銷售、物流四大行業範本，開箱即用的智慧體方案。',
    },
  },
}
