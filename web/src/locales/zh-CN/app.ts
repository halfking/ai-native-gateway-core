// app.ts — App.vue 外壳框架文案（顶栏角色、侧栏折叠、退出、语言切换、主题）。
export default {
  brand: {
    siteTitle: 'AI Native 组织核心网关',
    siteTitleLineTwo: '组织核心网关',
    alt: '开轩启圭标志',
  },
  role: {
    super_admin: '超级管理员',
    tenant_admin: '租户管理员',
  },
  // 2026-07-21: 与 ai-native-maintain / ai-session-manager 主题文案对齐。
  theme: {
    switchToLight: '切到浅色',
    switchToDark: '切到深色',
    lightTitle: '浅色模式',
    darkTitle: '深色模式',
  },
  // 2026-07-22: 顶部水平菜单的无障碍文案 + skip-link。
  // 2026-07-22: 公开壳页面底部文案（LifecycleShell.footer 三段）。
  // 2026-07-22: 用户菜单下拉（UserMenuDropdown）+ 用户信息弹窗（UserInfoDialog）。
  userMenu: {
    profile: '个人信息',
    changePassword: '修改密码',
    logout: '退出登录',
  },
  userInfo: {
    displayName: '显示名',
    username: '用户名',
    email: '邮箱',
    role: '角色',
    tenant: '租户',
    close: '关闭',
  },
  dataHorizon: {
    title: '流量数据记录滞后：网关有请求，会话流水未跟上',
    titleUnavailable: '无法确认流量数据状态：本页数字可能已过期',
    hintLabel: '排查',
    retry: '重新检查',
    failedHint: '读取数据状态失败。未确认状态不等于状态正常——本页数字请按「可能已过期」对待。',
  },
  deploySeq: {
    bannerTitle: '发现新版本',
    bannerHint: '页面已有更新，建议立即切换（重新加载页面即完成升级）',
    updateNow: '立即更新',
    later: '稍后',
    currentSeq: '页面版本序号',
    state: '更新状态',
    stateLatest: '已是最新',
    stateAvailable: '有新版本',
    stateIndeterminate: '不可判定',
    stateChecking: '正在检查…',
    reasonNoLocal: '本页未携带部署序号（构建期未注入）',
    reasonNoRemote: '服务端未返回部署序号（旧构建或非本仓托管）',
    reasonNetwork: '检查请求失败，无法判定',
    checks: '已检查次数',
    checkNow: '检查更新',
    sourceUnknown: '未注入',
  },
}
