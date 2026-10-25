// login.ts — login modal + guest header login entry.
export default {
  title: 'Sign in to control plane',
  hint: 'On first sign-in, or after an admin resets your password, change it before you continue.',
  password: 'Password',
  submit: 'Sign in',
  submitting: 'Signing in…',
  error: {
    required: 'Please enter username and password',
    failed: 'Sign-in failed',
    apiKeyUnsupported: 'This account no longer signs in with an API key. Use your username and password instead. Ask an administrator for help.',
    badResponse: 'The sign-in response was malformed. Try again, or contact an administrator.',
  },
  checking: 'Checking login status…',
}
