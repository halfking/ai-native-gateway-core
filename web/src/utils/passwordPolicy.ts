export interface PasswordRequirement {
  key: 'length' | 'upper' | 'lower' | 'digit'
  label: string
  passed: boolean
}

export interface PasswordPolicyCheck {
  valid: boolean
  requirements: PasswordRequirement[]
}

export function passwordsMatch(password: string, confirmPassword: string): boolean {
  return !!confirmPassword && password === confirmPassword
}

export function checkPasswordPolicy(password: string): PasswordPolicyCheck {
  const requirements: PasswordRequirement[] = [
    { key: 'length', label: '至少 8 位', passed: password.length >= 8 },
    { key: 'upper', label: '包含大写字母', passed: /[A-Z]/.test(password) },
    { key: 'lower', label: '包含小写字母', passed: /[a-z]/.test(password) },
    { key: 'digit', label: '包含数字', passed: /\d/.test(password) },
  ]

  return {
    valid: requirements.every((item) => item.passed),
    requirements,
  }
}
