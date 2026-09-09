// freeDiscovery.ts — 免费资源自动发现页 (FreeDiscoveryView) 文案。
// 命名空间：page / common / tabs / presets / form / orbi / tpl / scan / task / res / status / trigger / hist。
// 技术术语保持不译：api_type、openai-completions、tos_verdict 取值 (ok/caution/ambiguous/avoid)、Orbi JSON。
export default {
  page: {
      title: '免费资源发现',
      desc: '配置供应商模板 → 一键扫描上游模型列表 → 人工审查 → 批量导入免费资源池（接入 OmniFree 配额追踪）。'
    },
  common: {
      refresh: '刷新',
      loading: '处理中…',
      empty: '暂无数据',
      actions: '操作',
      enabled: '已启用',
      disabled: '已停用',
      hideForm: '收起'
    },
  tabs: {
      templates: '模板管理',
      tasks: '任务与结果审查',
      history: '导入历史'
    },
  presets: {
      title: '内置供应商预设',
      create: '一键创建',
      exists: '已创建',
      createDone: '已从预设创建模板：{name}',
      scannerPending: '该协议扫描器适配中，扫描暂会失败',
      keyless: 'keyless（无需 Key）'
    },
  form: {
      show: '+ 自定义模板',
      providerCode: 'Provider Code *',
      displayName: '显示名称',
      baseUrl: 'Base URL *',
      apiType: 'API 协议',
      apiKeyEnv: 'API Key 环境变量',
      apiKeyEnvHint: 'Key 本身不入库：填 $VAR 形式的环境变量引用（如 $GROQ_API_KEY），留空表示 keyless。',
      tosVerdict: 'ToS 初判',
      submit: '创建模板',
      created: '模板已创建'
    },
  orbi: {
      show: '导入 Orbi 模板 JSON',
      hint: '粘贴 Orbi pi-providers 模板文件内容（顶层含 providers 键的 JSON），单文件可含多个 provider，单个失败不阻断其余。',
      import: '导入',
      invalidJson: 'JSON 解析失败，请检查格式',
      done: '导入完成：成功 {created} 个，失败 {failed} 个'
    },
  tpl: {
      listTitle: '模板列表（{n}）',
      name: '模板',
      baseUrl: 'Base URL',
      apiType: 'API 协议',
      keyEnv: 'Key 环境变量',
      tos: 'ToS',
      enabled: '启用',
      createdAt: '创建时间',
      scan: '扫描',
      delete: '删除',
      deleteConfirm: '确认删除模板「{name}」？已产生的发现任务与结果不受影响。',
      deleted: '模板已删除：{name}'
    },
  scan: {
      title: '触发发现',
      pickTemplate: '选择已启用的模板…',
      start: '开始扫描',
      running: '扫描中…',
      done: '扫描完成：发现 {n} 个模型，请审查结果',
      failed: '扫描失败'
    },
  task: {
      listTitle: '发现任务（{n}）',
      provider: '供应商',
      status: '状态',
      trigger: '触发方式',
      found: '发现数',
      imported: '已导入',
      by: '操作人',
      time: '创建时间',
      error: '错误信息',
      review: '审查结果'
    },
  res: {
      title: '发现结果 · 任务 {id} · {provider}',
      pending: '待审查',
      all: '全部',
      model: '模型 ID',
      displayName: '显示名',
      freeType: '免费类型',
      monthly: '月配额 (tokens)',
      daily: '日配额 (tokens)',
      tos: 'ToS',
      importStatus: '导入状态',
      none: '该筛选条件下暂无结果',
      policy: '冲突策略',
      policySkip: 'skip：保留现有条目',
      policyOverwrite: 'overwrite：覆盖并重新启用',
      policyMerge: 'merge：仅补空字段',
      importSelected: '导入所选（{n}）',
      importAllPending: '导入全部待审查',
      importedToast: '导入完成：成功 {imported}，跳过 {skipped}，冲突 {conflicted}，失败 {failed}'
    },
  status: {
      taskPending: '等待',
      running: '运行中',
      success: '成功',
      failed: '失败',
      review: '待审查',
      imported: '已导入',
      skipped: '已跳过',
      conflict: '冲突'
    },
  trigger: {
      manual: '手动',
      scheduled: '定时',
      webhook: 'Webhook'
    },
  hist: {
      title: '导入历史（{n}）',
      desc: '展示产生过实际导入的任务（按导入数 > 0 过滤）；点击「明细」查看该任务全部结果的最终去向。',
      completed: '完成时间',
      detail: '明细',
      none: '暂无导入记录。完成一次「任务与结果审查」页的批量导入后会在这里显示。',
      detailTitle: '导入明细 · 任务 {id} · {provider}',
      importedAt: '导入时间'
    },
}
