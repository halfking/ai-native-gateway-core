// login.ts — login modal + guest header login entry.
export default {
  title: 'Sign in to control plane',
  subtitle: 'Kaixuan MaaS Console', // brand "开轩" kept consistent across locales
  username: 'Username',
  usernamePlaceholder: 'admin',
  password: 'Password',
  passwordPlaceholder: '••••••••',
  submit: 'Sign in',
  submitting: 'Signing in…',
  cancel: 'Cancel',
  close: 'Close',
  signIn: 'Sign in',
  error: {
    required: 'Please enter username and password',
    failed: 'Sign-in failed',
  },
  changePassword: "[TODO: login.changePassword]",
  passwordChangeSuccess: "[TODO: login.passwordChangeSuccess]",
  // 2026-07-09: First-load auth probe message
  checking: 'Checking login status…',
}
