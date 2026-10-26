// Chinese (Simplified) locale for ClientConfigDialog
export default {
  title: '{tool} 配置生成器',
  close: '关闭',

  step1: {
    title: "① 选择可用 API Key",
    refresh: '刷新',
    refreshing: '刷新中…',
    loading: '加载中…',
    empty: {
      title: '暂无 API Key',
      description: '当前租户下还没有可用的 API Key。管理员可在下方申请一个新的 Key。',
      applyButton: '申请新密钥',
    },
    selected: '已选：',
  },

  step2: {
    title: '② 操作系统',
    pathHint: "配置入口：",
  },

  step3: {
    title: "④ 选择模型范围",
    featured: "特色模型（所选密钥租户的路由配置）",
    all: '全部可用模型（从网关拉取）',
    featuredPreview: {
      empty: "所选租户尚未配置特色模型",
      manageButton: '前往「路由策略」配置 →',
      manage: '管理 →',
    },
    allModels: {
      selectAll: '全选当前',
      deselectAll: '清空',
      selected: '已选',
      filterHint: '（{count} 匹配搜索）',
      searchPlaceholder: '🔍 搜索模型名称 / 厂商 / family…',
      noMatch: '没有匹配的模型',
    },
  },

  retry: "重试",
  otherFamily: "其它",
  gateway: {
    title: "③ 网关 API 地址",
    hint: "默认使用当前网关站点地址。配置中会包含所选 API Key，请妥善保管生成文件和脚本。",
  },
  provider: {
    label: "供应商名称",
    hint: "同名且同地址时更新原项。若地址不同，脚本会停止并提示改名。",
  },
  notes: {
    cherryStudio: "请在客户端设置面板填入所选密钥；此文件仅为参数清单。",
    rooCode: "在 Roo Code 设置面板配置 OpenAI Compatible。",
  },

  footer: {
    generated: '已生成 {count} 个模型配置',
    generate: '生成配置',
    generating: '生成中…',
    regenerate: '重新生成',
  },

  results: {
    tabs: {
      yaml: "YAML 片段",
      file: '配置文件',
      script: '配置脚本',
      manual: '手动步骤',
    },
    actions: {
      scriptWarning: "脚本包含密钥，请下载后在本机执行。生成的脚本不在页面展开。",
      mergeHint: "脚本先备份旧配置，再合并 {provider}，保留其他供应商",
      copyContent: '复制内容',
      downloadFile: '下载文件',
      downloadScript: '下载脚本',
    },
  },

  applyDialog: {
    title: '申请新 API Key',
    applicationCode: '应用标识 (application_code)',
    applicationCodePlaceholder: '例如: default-app',
    description: '备注 (description)',
    descriptionPlaceholder: '可选：申请原因 / 用途',
    cancel: '取消',
    submit: '提交申请',
    submitting: '提交中…',
  },

  error: {
    invalidGatewayURL: "API 地址须为有效的 HTTP(S) URL，且不能包含账号、查询参数或片段。",
    invalidProviderName: "供应商名称须为 1–64 个英文字母、数字、下划线或连字符，且以字母或数字开头。",
    featuredLoadFailed: "特色模型加载失败，请重试。",
    catalogLoadFailed: "模型目录加载失败，请重试。",
    selectModel: "请至少选择一个模型；特色模型为空时请先到「路由策略」配置。",
    keyUnavailable: "所选密钥无法读取，请重新选择。",
    generateFailed: "配置生成失败，请重试。",
    copyFailed: "复制失败，请下载文件。",
    applyFailed: '申请失败',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor 不支持文件写入，请在 Settings UI 中手动配置',
}
