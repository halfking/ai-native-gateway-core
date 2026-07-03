// Updated: 2026-07-03 19:40 - Fixed vue-i18n emoji issue
export default { 
  // 通用
  login: '登录',
  logout: '退出',
  changePassword: '修改密码',
  cancel: '取消',
  confirm: '确认',
  save: '保存',
  delete: '删除',
  edit: '编辑',
  search: '搜索',
  reset: '重置',
  submit: '提交',
  back: '返回',
  next: '下一步',
  previous: '上一步',
  close: '关闭',
  
  // 用户角色
  role: {
    super_admin: '超级管理员',
    tenant_admin: '租户管理员',
  },
  
  // 导航栏
  nav: {
    collapseSidebar: '收起菜单',
    expandSidebar: '展开侧栏',
  },
  
  // 密码
  password: {
    changeSuccess: '密码修改成功',
    changeFailed: '密码修改失败',
  },
  
  // 版本信息
  version: '版本 ',
  build: '构建',

  // 2026-07-02 (request-logs 附件展示): 附件相关文案，对齐参考文档 §6。
  requests: {
    list: {
      table: {
        attachmentsTitle: '附件',
        noAttachments: '无附件',
      },
    },
    detail_extra: {
      attachmentsTab: '附件',
      attachmentsLoading: '加载附件中…',
      noAttachments: '无附件',
      clickToPreviewTitle: '点击查看大图',
      download: '下载',
      downloadOriginal: '下载原图',
      closePreview: '关闭',
    },
  },

  // 2026-07-03 (实时请求流): Dashboard swim lane 文案。
  dashboard: {
    liveStream: {
      title: '实时请求流',
      connected: '已连接',
      connecting: '连接中…',
      reconnecting: '重新连接中…',
      disconnected: '已断开',
      unsupported: '浏览器不支持',
      pause: '暂停',
      resume: '继续',
      filterAll: '全部状态',
      filterSuccess: '仅成功',
      filterInProgress: '进行中',
      filterGroupFailures: '失败分类',
      filterFailure5xx: '服务端 / 上游 (5xx)',
      filterFailure4xx: '客户端 / 鉴权 (4xx)',
      filterFailureTimeout: '超时 / 网络',
      filterFailureNotFound: '路由 / 模型未找到',
      filterFailureOther: '其它失败',
      empty: '等待请求进入…',
      countTooltip: '缓冲区内 {buffer} / 屏上可见 {visible}',
      countAria: '缓冲区内 {buffer} 个请求，屏上可见 {visible}',
      legend: {
        model: '模型族',
        status: '状态',
        openai: 'OpenAI',
        anthropic: 'Anthropic',
        domestic: '国产',
        oss: '开源',
        other: '其他',
        success: '成功',
        inProgress: '进行中',
        failure: '失败',
      },
    },
  },
}