// login.ts — 登录弹窗 + 访客头部登录入口。
export default {
  title: '登录控制面',
  hint: '首次登录或管理员重置密码后，需要先修改密码才能继续使用。',
  password: '密码',
  submit: '登录',
  submitting: '登录中…',
  error: {
    required: '请输入用户名和密码',
    failed: '没能登录，请检查用户名和密码后重试',
    apiKeyUnsupported: '此账号不再支持 API key 登录，请使用用户名/密码登录。如需帮助请联系管理员。',
    badResponse: '登录响应格式异常，请重试或联系管理员。',
  },
  checking: '正在检测登录状态…',
}
