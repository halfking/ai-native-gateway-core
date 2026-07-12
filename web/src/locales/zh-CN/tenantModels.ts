// tenantModels.ts — /tenant/models 页面文案（租户视角：标准模型目录 + 价格）。
// 命名空间：tenants.models.* / tenants.modelsModalities.* / tenants.modelsBilling.*
export default {
  page: {
    title: '标准模型',
    desc: '我们为您的租户开放的标准模型清单，按模型原厂分组。计费含输入 / 输出 / 缓存读 / 缓存写，均按每百万 Token 扣积分。',
    filterTitle: '标准模型 · 筛选',
    filterPlaceholder: '选择标准模型…',
    modelCount: '{n} 个模型',
    refresh: '刷新',
    loading: '加载中…',
    empty: '暂无可用模型',
    loadFailed: '加载失败',
    vendorSectionTitle: '模型清单',
  },
  columns: {
    model: '标准模型',
    canonicalName: '模型 ID',
    family: '分类',
    contextWindow: '上下文',
    multimodal: '多模态',
    modalityTag: '类型',
    billingMode: '计费模式',
    inPrice: '输入/1M',
    outPrice: '输出/1M',
    cacheIn: '缓存读/1M',
    cacheOut: '缓存写/1M',
  },
  modalities: {
    text: '文本',
    vision: '视觉',
    audio: '音频',
    multimodal: '多模态',
    embedding: '向量',
  },
  billing: {
    token: '按 Token 积分',
  },
  multimodal: {
    yes: '支持',
    no: '仅文本',
  },
  context: {
    notSet: '—',
  },
  filterBar: {
    selectedVendor: '已选厂商：{vendor}',
    count: '已显示 {n} / 共 {m}',
  },
  // 通用 CTA（仅在本页用到）
  cta: {
    buyCredits: '购买积分',
  },
}