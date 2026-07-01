import { createI18n } from 'vue-i18n'
import en from './locales/en'
import zhCN from './locales/zh-CN'
import ja from './locales/ja'
import de from './locales/de'
import fr from './locales/fr'
import es from './locales/es'
import zhTW from './locales/zh-TW'
import ar from './locales/ar'

// 从 localStorage 获取用户语言偏好，默认简体中文
const savedLocale = localStorage.getItem('llmgw_locale') || 'zh-CN'

export const i18n = createI18n({
  legacy: false, // 使用 Composition API 模式
  locale: savedLocale,
  fallbackLocale: 'en',
  messages: {
    en,
    'zh-CN': zhCN,
    ja,
    de,
    fr,
    es,
    'zh-TW': zhTW,
    ar,
  },
})

// 语言配置
export const languages = [
  { code: 'zh-CN', name: '简体中文', flag: '🇨🇳' },
  { code: 'en', name: 'English', flag: '🇺🇸' },
  { code: 'ja', name: '日本語', flag: '🇯🇵' },
  { code: 'de', name: 'Deutsch', flag: '🇩🇪' },
  { code: 'fr', name: 'Français', flag: '🇫🇷' },
  { code: 'es', name: 'Español', flag: '🇪🇸' },
  { code: 'zh-TW', name: '繁體中文', flag: '🇹🇼' },
  { code: 'ar', name: 'العربية', flag: '🇸🇦' },
]
