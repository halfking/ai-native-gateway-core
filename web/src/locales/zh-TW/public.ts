// public.ts — 公開入口文案（下載 / 捐贈 / 離線啟用）
//
// 2026-10-25 重寫（impeccable craft-floor / STYLE 輪）：
// 原稿整份還是英文，現在全文翻成台灣用語，並去掉裝飾用的「·」分隔、
// 以破折號代替標點，以及「可審計、可客製、可私有化」這種三詞排比。
export default {
  download: {
    activateLink: '安裝後啟用精靈',
  },
  support: {
    emailPlaceholder: "you{'@'}company.com",
  },
  layout: {
    download: '下載',
    support: '支持我們',
  },
  agreement: {
    title: '使用者協議與使用須知',
    intro: '歡迎使用 AI-Native 組織核心閘道。本軟體核心以開源方式分享，標準版按「現狀提供（AS IS）」原則供您自行部署與使用。繼續之前，請先看下面四點：',
    points: [
      '權利與義務：您負責在自有環境裡的安裝、設定、備份與合規使用。我們提供軟體與文件，不對業務結果、模型輸出品質或第三方服務的可用性作保證。',
      '資料蒐集：預設不蒐集業務內容。選用的執行遙測只包含版本、執行個體標識、License 狀態與彙整指標，不蒐集提示詞、會話原文、API Key 或可直接識別到個人的資料。',
      '開源分享：非客製版本以社群分享為目的，不含商業 SLA。需要客製時，請透過支援頁與我們另行約定。',
      '免責範圍：軟體按現狀提供。我們不承擔間接損失、服務中斷、資料遺失或 AI 決策帶來的後果，風險請您自己評估並做好防護。',
    ],
    disclaimer: '完整條款見使用者協議全文。勾選即表示您已閱讀並理解上述範圍。',
    checkbox: '我已閱讀並同意',
    fullLink: '《使用者協議與資料處理授權》',
    accept: '同意並繼續',
  },
  opAgreement: {
    downloadTitle: '下載前請先閱讀 {title}',
    activateTitle: '啟用前請先閱讀 {title}',
  },
}
