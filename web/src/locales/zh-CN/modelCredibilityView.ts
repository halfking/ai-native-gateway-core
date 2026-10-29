// modelCredibilityView.ts — 模型可信度（防掺水）页 (zh-CN)。
//
// 页面回答一个问题：**这个供应商给我的，是不是它声称的那个模型？**
// 四个子分数（身份/能力/一致性/参考基线）分开显示，是为了让运维能看出
// 是**哪一项测量**导致了结论 —— 一个不透明的总分与「猜的」没有区别。
export default {
  pageTitle: '模型可信度',
  pageSubtitle:
    '检测上游是否在以低配模型冒充高配模型（掺水/偷换/降智）。判定依据四把尺：上游自报身份、' +
    '同款模型的能力对比、请求签名是否漂移、外部基线校准。测不出结论时标为「证据不足」，不当作「没问题」。',

  stats: {
    total: '已判定节点',
    verified: '已验证',
    suspected: '存疑',
    diluted: '渗水',
    unknown: '证据不足',
    providers: '涉及供应商',
  },

  filter: {
    verdict: '结论',
    verdictPlaceholder: '全部结论',
    model: '模型',
    modelPlaceholder: '按模型名过滤',
    query: '查询',
    refresh: '刷新',
    scanAll: '全量检测',
    scanning: '检测中…',
    sort: '排序',
    sortTime: '按检测时间（最新在前）',
    sortName: '按模型名',
    sortScore: '按可信度（低→高）',
  },

  verdict: {
    verified: '已验证',
    suspected: '存疑',
    diluted: '渗水',
    unknown: '证据不足',
  },

  dimension: {
    identity: '身份自洽',
    capability: '能力水位',
    consistency: '指纹一致',
    reference: '参考基线',
  },

  table: {
    provider: '供应商',
    credential: '凭据',
    model: '模型',
    credibility: '可信度',
    verdict: '结论',
    dimensions: '证据维度',
    measuredAt: '检测时间',
    action: '操作',
  },

  status: {
    success: '正常',
    partial: '部分',
    failed: '探测失败',
    inconclusive: '无法判定',
    unknown: '未检测',
  },

  anomaly: {
    identity_model_name_mismatch: '上游自报的模型名与请求不符',
    identity_family_mismatch: '上游自报的模型与请求模型不是同一厂商',
    identity_reported_model_absent: '上游未返回模型名，无法自证身份',
    capability_below_peer_best: '能力低于同款模型的最好水平',
    capability_severely_below_peer_best: '能力显著低于同款模型的最好水平',
    capability_above_peer_best: '能力高于同款模型的最好水平',
    consistency_prompt_token_drift: '请求签名漂移，后端可能已被替换',
    consistency_system_fingerprint_drift: '上游指纹发生变化',
    reference_below_expected_accuracy: '低于外部基线给出的期望水平',
    capability_no_measurement: '没有可用的能力实测数据',
    capability_peer_sample_too_small: '同款模型的对照样本不足',
    identity_probe_failed: '身份探测请求失败',
    identity_probe_empty_response: '身份探测没有得到回答',
  },

  detail: {
    title: '判定详情',
    evidence: '判定证据',
    anomalies: '异常项',
    history: '历史判定',
    noHistory: '暂无历史判定记录',
    requestedModel: '请求模型',
    reportedModel: '上游自报模型',
    promptSignature: '请求签名（prompt token 计数）',
    fingerprint: '上游指纹',
    measuredDimensions: '本次有数据的维度',
    close: '关闭',
  },

  empty: {
    noData: '还没有可信度判定数据。',
    hint: '判定由后台定时扫描产生（每个节点每轮一次极小的探测请求）；也可以点「全量检测」立即跑一轮。',
  },

  error: {
    loadFailed: '模型可信度数据没能加载出来，请重试',
    scanFailed: '模型可信度检测没能完成，请重试',
    detailFailed: '模型可信度判定详情没能加载出来，请重试',
  },
}
