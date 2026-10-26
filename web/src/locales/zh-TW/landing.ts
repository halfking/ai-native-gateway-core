// landing.ts — 落地頁文案（未登錄首頁）。
//
// 2026-10-25 改寫（impeccable craft-floor / STYLE 輪）：
// 對齊 zh-CN / en-US 的新版樣板。原稿是三段式行銷串（`內核開源 · 企業級 · 私有部署`）、
// 形容詞代替事實（高可用體系 / 智慧 / 護盾）、破折號代替標點、每段恰好三件事。
// 規則見倉庫根目錄 DESIGN.md §7「Voice」。
//
// 事實紀律：只保留產品確實做到的事（Apache 2.0 / 40 MB 映像檔 / 200 併發 P99 < 500 ms /
// RLS 列層級安全 / 43 輪稽核 L1=0 / k3s + Docker / 三套入向協定 / 15 天試用）。
// 台灣用語：伺服器、資料庫、資料、憑證、稽核、協定、映像檔。
export default {
  // 只在 PublicPageShell 頁腳用到（品牌名 + 本項），已不再作為標題上方的小標籤渲染。
  kicker: 'Apache 2.0 開源核心',
  brandTitle: 'AI-Native 組織核心閘道',
  title: '把公司裡所有的大模型呼叫，收進一個能管、能查、能關的入口',
  subtitle:
    'OpenAI、Anthropic、Responses 三套協定接進同一個閘道，依租戶與模型選路，計費、稽核、限流都在這一處。部署在你自己的機器上，資料不出門。',
  featuresTitle: '核心功能',
  featuresSubtitle: '接入、選路、計費、稽核，都在這一個入口裡',
  // 功能導軌（右欄）的六條：每一條都驗得掉，不寫形容詞。
  heroPoints: [
    '核心採 Apache 2.0 授權',
    '資料面以 Go 撰寫，映像檔 40 MB',
    '併發 200，P99 低於 500 ms',
    '三套入向協定統一接入',
    'PostgreSQL RLS 列層級隔離',
    'k3s 或 Docker，裝在自己的機器上',
  ],
  features: {
    smartRouting: {
      title: '依租戶與任務選路',
      description: '依租戶、模型、任務類型自動選路。上游出問題時，探測到就換下一個，憑證指紋池負責輪替。',
    },
    safety: {
      title: '提示注入與敏感資料',
      description: 'LLM-as-judge 偵測提示詞注入（v1 只觀測、不攔截），敏感欄位依規則遮蔽。',
      badge: 'beta',
    },
    cache: {
      title: '快取與成本',
      description: '前綴穩定化加上語意快取，KV Cache 命中率提高，token 的花費就降下來。',
    },
    agent: {
      title: 'Agent 與 MCP 接入',
      description: 'Agent 註冊中心、A2A 協定、MCP 工具託管與協定轉換，從代理 LLM 走向編排 Agent。',
      badge: '即將推出',
    },
    observability: {
      title: '每一筆請求都留痕',
      description: '請求日誌、路由決策稽核、OTel 鏈路追蹤、SIEM/CEF 事件匯出。出事時查得到來龍去脈。',
    },
    billing: {
      title: '用量與帳單對得上',
      description: '方案、點數、三池錢包（訂閱、信用、儲值）。租戶自己就能查用量，不用問管理員。',
    },
    multiProtocol: {
      title: '三套協定，一個入口',
      description: 'OpenAI Chat、Anthropic Messages、Responses 入向統一。客戶端照舊寫，閘道不用改。',
    },
    multiTenant: {
      title: '租戶之間不串',
      description: 'PostgreSQL RLS 列層級安全，43 輪稽核 L1=0，每個租戶各自有策略與配額。',
    },
  },
  advantagesTitle: '為什麼自己部署',
  advantagesSubtitle: '資料要留在自己家裡，帳也要自己看得懂',
  advantages: {
    openSource: {
      title: '核心開源',
      description: '資料面、控制面、認證、路由排程全部開源，Apache 2.0。改得動，也審得清。',
    },
    private: {
      title: '私有部署',
      description: '裝在你自己的機器上，k3s 與 Docker 兩種形式，不依賴任何外部服務。',
    },
    antiBan: {
      title: '上游封鎖也不會斷',
      description: '多憑證輪替加上主動探測：某張憑證被封，自動換下一張繼續跑。',
    },
    perf: {
      title: '扛得住的 Go 資料面',
      description: '原生 Go，映像檔 40 MB，併發 200 時 P99 仍低於 500 ms，SSE 串流轉發穩定。',
    },
  },
  footer: '核心採 Apache 2.0 授權開源，部署在你自己的機器上。',
  roadmap: {
    title: '產品演進路線',
    subtitle: '從 LLM 資料面走到企業 Agent 閘道',
    v31: {
      phase: 'v3.1',
      title: 'API Hub 資產中心 + MCP 工具託管',
      description: 'LLM 端點、MCP 服務與 Agent 統一登記，開發者自助查找與再利用。',
    },
    v32: {
      phase: 'v3.2',
      title: '注入攔截正式版 + SIEM 串接 + SpecBoost',
      description: '提示詞注入從只觀測升級為可攔截，敏感資料遮蔽，API 描述自動豐富化以提高 Function Calling 準確率。',
    },
    v40: {
      phase: 'v4.0',
      title: 'Agent 註冊中心 + A2A 協定閘道',
      description: '智慧體之間互相委派任務，OpenClaw 與自建 Agent 走同一個入口接進來。',
    },
    v50: {
      phase: 'v5.0',
      title: '產業方案正式版',
      description: '客服、人資、銷售、物流四套範本，裝上就能用。',
    },
  },
  // 2026-07-21: guest-nav aria + 頂部 7 個下載/啟用連結（App.vue L197-203）。
  guestNavAria: '產品導覽',
  navDownload: '下載',
  navSetup: '安裝與啟用',
  navActivate: '線上啟用',
  navLicense: '授權狀態',
  navAgreement: '使用者協議',
  navSupport: '技術支援',
  // 2026-07-21: 落地頁主 CTA（LandingView.vue L109-119）。
  ctaLogin: '登入控制面',
  ctaDownload: '下載安裝包',
  ctaActivate: '啟用 License',
  ctaAgreement: '檢視使用者協議',
}
