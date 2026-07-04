// app.ts — App.vue 外壳框架文案（顶栏角色、侧栏折叠、退出、语言切换）。
export default {
  brand: 'LLM Gateway',
  role: {
    super_admin: '超级管理员',
    tenant_admin: '租户管理员',
  },
  sidebar: {
    expand: '展开侧栏',
    collapse: '收起侧栏',
    collapseMenu: '收起菜单',
  },
  logout: '退出',
  lang: {
    switch: '切换语言',
    label: '语言',
  },
}
