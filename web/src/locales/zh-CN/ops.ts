export default {


  overview: {
    nodeDetailSuperAdminOnlyHint: '仅超级管理员可以维护节点；当前以只读方式展示。',
    nodeDetailDefaultTenantOnlyHint: '仅 default 租户可以维护节点；当前以只读方式展示。',
    pingFailedWithReason: '会话 Ping 失败：{reason}。请重试',
    saveSettingFailedWithReason: '设置没能保存：{reason}。请重试',
    maintainFailed: '维护没能完成，请重试',
    modelMaintainFailed: '模型维护没能完成，请重试',
    fpSlotLoadFailed: '指纹槽位没能加载出来，请重试',
    fpSlotProviderMissing: '缺少 provider_id，指纹槽位没能加载。请先选择供应商再试',
    accessErrorsHint: '失败调用 + 路由错误 · 打开原始请求详情',
    recentRequestsHint: '最近 1 小时{source} · 查看请求详情',
  },


  // License Management

  // Fault Management

  // Auto Update

  // Center Operations

  // VibeCoding
  vibecoding: {
    title: 'VibeCoding',
    loadFailed: '数据没能加载出来，请重试',
    fillRequired: '请填写必填项',
    projects: '项目',
    sessions: '会话',
    codeReviews: '代码审查',
    createProject: '创建项目',
    createProjectTitle: '创建新项目',
    createProjectSuccess: '项目创建成功',
    createProjectFailed: '项目没能创建出来，请重试',
    newSession: '新会话',
    createSessionTitle: '创建新会话',
    createSessionSuccess: '会话创建成功',
    createSessionFailed: '会话没能创建出来，请重试',
    viewSessions: '查看会话',
    viewReviews: '查看审查',
    showAll: '显示全部',
    reviewDetail: '审查详情',
    /**
     * 空状态（2026-10-27 第 14 增量 · 空状态 5 分）。
     * ★ 三张表原先共用一句 `hyper.list.empty`（「列表暂时没有内容」），但它们是**两种**状态：
     *   · projects —— 用的是原始数组 ⇒ 空 = 还没创建过任何项目（FirstUse）
     *   · sessions / reviews —— 用的是**筛选后**的数组 ⇒ 空 = 被筛掉了（Filtered）
     *   ⇒ 同一句话覆盖两种状态，等于把「你还没开始」和「你的筛选条件太窄」讲成同一件事。
     */
    emptyFirstUseProjects: '还没有创建任何项目。',
    emptyFilteredSessions: '当前筛选条件下没有会话记录。',
    emptyFilteredReviews: '当前筛选条件下没有代码审查记录。',
    projectName: '项目名称',
    projectNamePlaceholder: '请输入项目名称',
    language: '编程语言',
    languagePlaceholder: '例如: TypeScript',
    framework: '框架',
    frameworkPlaceholder: '例如: Vue 3',
    projectId: '项目ID',
    sessionName: '会话名称',
    taskType: '任务类型',
    taskTypePlaceholder: '例如: code review, refactor, explain',
    startedAt: '开始时间',
    endedAt: '结束时间',
    filePath: '文件路径',
    score: '评分',
    summary: '总结',
    issues: '问题',
    suggestions: '建议',
    category: '分类',
    reviewedAt: '审查时间',
    line: '行号',
    severity: '严重度',
    message: '消息',
  },
}
