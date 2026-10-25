// login.ts — login modal + guest header login entry.
export default {
  title: 'Sign in to control plane',
  subtitle: 'Kaixuan MaaS Console', // brand "开轩" kept consistent across locales
  hint: 'On first sign-in, or after an admin resets your password, change it before you continue.',
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
    apiKeyUnsupported: 'This account no longer signs in with an API key. Use your username and password instead. Ask an administrator for help.',
    badResponse: 'The sign-in response was malformed. Try again, or contact an administrator.',
  },
  changePassword: 'Change password',
  passwordChangeSuccess: 'Password changed successfully',
  passwordChangedReloading: 'Password updated, signing out and back in…',
  passwordChangedReLogin: 'Password updated. Please sign in with your new password.',
  // 2026-07-09: First-load auth probe message
  checking: 'Checking login status…',
}
