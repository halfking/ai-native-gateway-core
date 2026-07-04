// users.ts — 用户管理页文案。复用 common 模块的通用词（启用/禁用、取消、确认、创建、删除等）。
export default {
  title: '用户管理',
  create: '新建用户',
  readOnlyNotice: '📖 您是租户管理员，当前为只读模式。用户管理仅限查看，不能创建、编辑或删除用户。',
  filter: {
    byTenant: '按租户过滤：',
    allTenants: '全部租户',
  },
  table: {
    username: '用户名',
    displayName: '显示名',
    email: '邮箱',
    tenant: '租户',
    role: '角色',
    lastLogin: '最后登录',
  },
  action: {
    resetPassword: '重置密码',
  },
  role: {
    super_admin: '超级管理员',
    tenant_admin: '租户管理员',
  },
  modal: {
    create: {
      title: '新建用户',
      username: '用户名 *',
      password: '密码 *',
      passwordPlaceholder: '至少8位',
      displayName: '显示名',
      email: '邮箱',
      tenant: '租户 *',
      role: '角色',
    },
    reset: {
      title: '重置密码 — {name}',
      newPassword: '新密码',
      passwordPlaceholder: '至少8位',
    },
  },
  error: {
    usernamePasswordRequired: '用户名和密码不能为空',
    createFailed: '创建失败',
    resetFailed: '重置失败',
    passwordMinLength: '密码至少8个字符',
  },
  confirmDelete: '确认删除用户 {name}？',
}
