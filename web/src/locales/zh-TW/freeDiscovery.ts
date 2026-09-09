// freeDiscovery.ts — free discovery page copy (zh-TW).
export default {
  page: {
      title: "免費資源發現",
      desc: "設定供應商模板 → 一鍵掃描上游模型列表 → 人工審查 → 批次匯入免費資源池（接入 OmniFree 配額追蹤）。"
    },
  common: {
      refresh: "重新整理",
      loading: "處理中…",
      empty: "暫無資料",
      actions: "操作",
      enabled: "已啟用",
      disabled: "已停用",
      hideForm: "收起"
    },
  tabs: {
      templates: "模板管理",
      tasks: "任務與結果審查",
      history: "匯入歷史"
    },
  presets: {
      title: "內建供應商預設",
      create: "一鍵建立",
      exists: "已建立",
      createDone: "已從預設建立模板：{name}",
      scannerPending: "該協定掃描器適配中，掃描暫會失敗",
      keyless: "keyless（無需 Key）"
    },
  form: {
      show: "+ 自訂模板",
      providerCode: "Provider Code *",
      displayName: "顯示名稱",
      baseUrl: "Base URL *",
      apiType: "API 協定",
      apiKeyEnv: "API Key 環境變數",
      apiKeyEnvHint: "Key 本身不入庫：填 $VAR 形式的環境變數參照（如 $GROQ_API_KEY），留空表示 keyless。",
      tosVerdict: "ToS 初判",
      submit: "建立模板",
      created: "模板已建立"
    },
  orbi: {
      show: "匯入 Orbi 模板 JSON",
      hint: "貼上 Orbi pi-providers 模板檔案內容（頂層含 providers 鍵的 JSON），單檔可含多個 provider，單個失敗不阻斷其餘。",
      import: "匯入",
      invalidJson: "JSON 解析失敗，請檢查格式",
      done: "匯入完成：成功 {created} 個，失敗 {failed} 個"
    },
  tpl: {
      listTitle: "模板列表（{n}）",
      name: "模板",
      baseUrl: "Base URL",
      apiType: "API 協定",
      keyEnv: "Key 環境變數",
      tos: "ToS",
      enabled: "啟用",
      createdAt: "建立時間",
      scan: "掃描",
      delete: "刪除",
      deleteConfirm: "確認刪除模板「{name}」？已產生的發現任務與結果不受影響。",
      deleted: "模板已刪除：{name}"
    },
  scan: {
      title: "觸發發現",
      pickTemplate: "選擇已啟用的模板…",
      start: "開始掃描",
      running: "掃描中…",
      done: "掃描完成：發現 {n} 個模型，請審查結果",
      failed: "掃描失敗"
    },
  task: {
      listTitle: "發現任務（{n}）",
      provider: "供應商",
      status: "狀態",
      trigger: "觸發方式",
      found: "發現數",
      imported: "已匯入",
      by: "操作人",
      time: "建立時間",
      error: "錯誤訊息",
      review: "審查結果"
    },
  res: {
      title: "發現結果 · 任務 {id} · {provider}",
      pending: "待審查",
      all: "全部",
      model: "模型 ID",
      displayName: "顯示名",
      freeType: "免費類型",
      monthly: "月配額 (tokens)",
      daily: "日配額 (tokens)",
      tos: "ToS",
      importStatus: "匯入狀態",
      none: "該篩選條件下暫無結果",
      policy: "衝突策略",
      policySkip: "skip：保留現有條目",
      policyOverwrite: "overwrite：覆蓋並重新啟用",
      policyMerge: "merge：僅補空欄位",
      importSelected: "匯入所選（{n}）",
      importAllPending: "匯入全部待審查",
      importedToast: "匯入完成：成功 {imported}，跳過 {skipped}，衝突 {conflicted}，失敗 {failed}"
    },
  status: {
      taskPending: "等待",
      running: "執行中",
      success: "成功",
      failed: "失敗",
      review: "待審查",
      imported: "已匯入",
      skipped: "已跳過",
      conflict: "衝突"
    },
  trigger: {
      manual: "手動",
      scheduled: "定時",
      webhook: "Webhook"
    },
  hist: {
      title: "匯入歷史（{n}）",
      desc: "展示產生過實際匯入的任務（按匯入數 > 0 過濾）；點擊「明細」查看該任務全部結果的最終去向。",
      completed: "完成時間",
      detail: "明細",
      none: "暫無匯入記錄。完成一次「任務與結果審查」頁的批次匯入後會在這裡顯示。",
      detailTitle: "匯入明細 · 任務 {id} · {provider}",
      importedAt: "匯入時間"
    },
}
