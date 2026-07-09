// login.ts — 登录弹窗 + 访客头部登录入口。
export default {
  title: '登录控制面',
  subtitle: '开轩 MaaS 管理后台', // "开轩" 品牌名跨语种保持不译
  username: '用户名',
  usernamePlaceholder: 'admin',
  password: '密码',
  passwordPlaceholder: '••••••••',
  submit: '登录',
  submitting: '登录中…',
  cancel: '取消',
  close: '关闭',
  signIn: '登录',
  error: {
    required: '请输入用户名和密码',
    failed: '登录失败',
  },
  changePassword: '修改密码',
  passwordChangeSuccess: '密码修改成功',
  // 2026-07-09: 首次进入页面时检测 cookie auth 状态的提示文案
  checking: '正在检测登录状态…',
}
