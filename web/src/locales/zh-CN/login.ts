// login.ts — 登录弹窗 + 访客头部登录入口。
export default {
  title: '登录控制面',
  subtitle: '开轩 MaaS 管理后台', // "开轩" 品牌名跨语种保持不译
  hint: '首次登录或管理员重置密码后，需要先修改密码才能继续使用。',
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
    apiKeyUnsupported: '此账号不再支持 API key 登录，请使用用户名/密码登录。如需帮助请联系管理员。',
    badResponse: '登录响应格式异常，请重试或联系管理员。',
  },
  changePassword: '修改密码',
  passwordChangeSuccess: '密码修改成功',
  passwordChangedReloading: '密码已更新，正在注销并重新登录…',
  passwordChangedReLogin: '密码已更新，请使用新密码重新登录。',
  // 2026-07-09: 首次进入页面时检测 cookie auth 状态的提示文案
  checking: '正在检测登录状态…',
}
