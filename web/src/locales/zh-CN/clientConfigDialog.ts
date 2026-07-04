// Chinese (Simplified) locale for ClientConfigDialog
export default {
  title: '{tool} 配置生成器',
  close: '关闭',
  
  step1: {
    title: '① 选择 API Key（当前租户下所有密钥）',
    refresh: '刷新',
    refreshing: '刷新中…',
    loading: '加载中…',
    empty: {
      title: '暂无 API Key',
      description: '当前租户下还没有可用的 API Key。管理员可点击下方按钮申请一个新 Key。',
      applyButton: '申请新密钥',
    },
    selected: '已选：',
  },
  
  step2: {
    title: '② 操作系统',
    pathHint: '配置文件路径：',
  },
  
  step3: {
    title: '③ 选择模型范围',
    featured: '热门模型（路由 featured 配置）',
    all: '全部可用模型（从网关拉取）',
    featuredPreview: {
      loading: '加载中…',
      empty: '路由中尚未配置热门模型',
      manageButton: '前往「路由策略」配置 →',
      manage: '管理 →',
    },
    allModels: {
      selectAll: '全选当前',
      deselectAll: '清空',
      selected: '已选',
      filterHint: '（{count} 匹配搜索）',
      searchPlaceholder: '🔍 搜索模型名称 / 厂商 / family…',
      loading: '加载中…',
      noMatch: '没有匹配的模型',
    },
  },
  
  footer: {
    generated: '已生成 {count} 个模型配置',
    generate: '生成配置',
    generating: '生成中…',
    regenerate: '重新生成',
  },
  
  results: {
    tabs: {
      file: '配置文件',
      script: '配置脚本',
      manual: '手动步骤',
    },
    actions: {
      copyContent: '复制内容',
      downloadFile: '下载文件',
      downloadScript: '下载脚本',
      scriptHint: '脚本自动备份旧配置文件',
    },
  },
  
  applyDialog: {
    title: '申请新 API Key',
    close: '关闭',
    applicationCode: '应用标识 (application_code)',
    applicationCodePlaceholder: '例如: default-app',
    description: '备注 (description)',
    descriptionPlaceholder: '可选：申请原因 / 用途',
    cancel: '取消',
    submit: '提交申请',
    submitting: '提交中…',
  },
  
  error: {
    applyFailed: '申请失败',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor 不支持文件写入，请在 Settings UI 中手动配置',
}
