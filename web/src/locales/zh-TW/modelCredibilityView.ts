// modelCredibilityView.ts — 模型可信度（防摻水）頁 (zh-TW)。
export default {
  pageTitle: '模型可信度',
  pageSubtitle:
    '偵測上游是否以低配模型冒充高配模型（摻水／偷換／降智）。判定依據四把尺：上游自報身分、' +
    '同款模型的能力對比、請求簽章是否漂移、外部基線校準。測不出結論時標為「證據不足」，不當作「沒問題」。',

  stats: {
    total: '已判定節點',
    verified: '已驗證',
    suspected: '存疑',
    diluted: '摻水',
    unknown: '證據不足',
    providers: '涉及供應商',
  },

  filter: {
    verdict: '結論',
    verdictPlaceholder: '全部結論',
    model: '模型',
    modelPlaceholder: '依模型名稱過濾',
    query: '查詢',
    refresh: '重新整理',
    scanAll: '全量檢測',
    scanning: '檢測中…',
    sort: '排序',
    sortTime: '依偵測時間（最新在前）',
    sortName: '依模型名稱',
    sortScore: '依可信度（低→高）',
  },

  verdict: {
    verified: '已驗證',
    suspected: '存疑',
    diluted: '摻水',
    unknown: '證據不足',
  },

  dimension: {
    identity: '身分自洽',
    capability: '能力水準',
    consistency: '指紋一致',
    reference: '參考基線',
  },

  table: {
    provider: '供應商',
    credential: '憑證',
    model: '模型',
    credibility: '可信度',
    verdict: '結論',
    dimensions: '證據維度',
    measuredAt: '偵測時間',
    action: '操作',
  },

  status: {
    success: '正常',
    partial: '部分',
    failed: '探測失敗',
    inconclusive: '無法判定',
    unknown: '未偵測',
  },

  anomaly: {
    identity_model_name_mismatch: '上游自報的模型名與請求不符',
    identity_family_mismatch: '上游自報的模型與請求模型不是同一家廠商',
    identity_reported_model_absent: '上游未回傳模型名，無法自證身分',
    capability_below_peer_best: '能力低於同款模型的最好水準',
    capability_severely_below_peer_best: '能力顯著低於同款模型的最好水準',
    capability_above_peer_best: '能力高於同款模型的最好水準',
    consistency_prompt_token_drift: '請求簽章漂移，後端可能已被替換',
    consistency_system_fingerprint_drift: '上游指紋發生變化',
    reference_below_expected_accuracy: '低於外部基線給出的預期水準',
    capability_no_measurement: '沒有可用的能力實測資料',
    capability_peer_sample_too_small: '同款模型的對照樣本不足',
    identity_probe_failed: '身分探測請求失敗',
    identity_probe_empty_response: '身分探測沒有得到回答',
  },

  detail: {
    title: '判定詳情',
    evidence: '判定證據',
    anomalies: '異常項目',
    history: '歷史判定',
    noHistory: '尚無歷史判定紀錄',
    requestedModel: '請求模型',
    reportedModel: '上游自報模型',
    promptSignature: '請求簽章（prompt token 計數）',
    fingerprint: '上游指紋',
    measuredDimensions: '本次有資料的維度',
    close: '關閉',
  },

  empty: {
    noData: '還沒有可信度判定資料。',
    hint: '判定由背景定時掃描產生（每個節點每輪一次極小的探測請求）；也可以按「全量檢測」立即跑一輪。',
  },

  error: {
    loadFailed: '模型可信度資料沒能載入出來，請重試',
    scanFailed: '模型可信度檢測沒能完成，請重試',
    detailFailed: '模型可信度判定詳情沒能載入出來，請重試',
  },
}
