#!/usr/bin/env node
// patch-simple-keys-i18n.mjs — 为 7 个语言的 simpleMode.ts 添加 keys 部分文案

import { readFileSync, writeFileSync } from 'fs'
import { fileURLToPath } from 'url'
import { dirname, join } from 'path'

const __dirname = dirname(fileURLToPath(import.meta.url))
const localesDir = join(__dirname, '../src/locales')

const translations = {
  'en-US': {
    subtitle: 'Issue a key with two fields; tenant and application are auto-filled with defaults',
    clientName: 'Client Name',
    clientNamePlaceholder: 'e.g., prod, dev, zhangsan-cli',
    clientNameHint: 'Unique identifier under the same tenant + application',
    clientNameRequired: 'Client name is required',
    remark: 'Remark',
    remarkPlaceholder: 'Explain the purpose, e.g., for testing, production',
    sign: 'Issue',
    signing: 'Issuing...',
    created: 'Key Created',
    copyHint: 'Please copy and save immediately; the complete key will not be shown again after closing',
    client: 'Client',
    signAnother: 'Issue Another',
    backToList: 'Back to Access List',
    tenant: 'Tenant',
    application: 'Application',
    autoFillHint: 'The following fields are auto-filled with defaults:',
    advancedHint: 'Need more options? Switch to expert mode for the full form.',
  },
  'ar-SA': {
    subtitle: 'إصدار مفتاح بحقلين؛ المستأجر والتطبيق يملآن تلقائياً بالقيم الافتراضية',
    clientName: 'اسم العميل',
    clientNamePlaceholder: 'على سبيل المثال: prod، dev، zhangsan-cli',
    clientNameHint: 'معرف فريد تحت نفس المستأجر + التطبيق',
    clientNameRequired: 'اسم العميل مطلوب',
    remark: 'ملاحظة',
    remarkPlaceholder: 'اشرح الغرض، على سبيل المثال: للاختبار، الإنتاج',
    sign: 'إصدار',
    signing: 'جاري الإصدار...',
    created: 'تم إنشاء المفتاح',
    copyHint: 'يرجى النسخ والحفظ فوراً؛ لن يظهر المفتاح الكامل مرة أخرى بعد الإغلاق',
    client: 'العميل',
    signAnother: 'إصدار آخر',
    backToList: 'العودة إلى قائمة الوصول',
    tenant: 'المستأجر',
    application: 'التطبيق',
    autoFillHint: 'الحقول التالية تملأ تلقائياً بالقيم الافتراضية:',
    advancedHint: 'هل تحتاج المزيد من الخيارات؟ التبديل إلى وضع الخبراء للحصول على النموذج الكامل.',
  },
  'de-DE': {
    subtitle: 'Schlüssel mit zwei Feldern ausstellen; Mandant und Anwendung werden automatisch mit Standardwerten gefüllt',
    clientName: 'Client-Name',
    clientNamePlaceholder: 'z.B. prod, dev, zhangsan-cli',
    clientNameHint: 'Eindeutige Kennung unter demselben Mandanten + Anwendung',
    clientNameRequired: 'Client-Name ist erforderlich',
    remark: 'Bemerkung',
    remarkPlaceholder: 'Zweck erklären, z.B. zum Testen, Produktion',
    sign: 'Ausstellen',
    signing: 'Wird ausgestellt...',
    created: 'Schlüssel erstellt',
    copyHint: 'Bitte sofort kopieren und speichern; der vollständige Schlüssel wird nach dem Schließen nicht mehr angezeigt',
    client: 'Client',
    signAnother: 'Noch einen ausstellen',
    backToList: 'Zurück zur Zugriffsliste',
    tenant: 'Mandant',
    application: 'Anwendung',
    autoFillHint: 'Die folgenden Felder werden automatisch mit Standardwerten gefüllt:',
    advancedHint: 'Benötigen Sie weitere Optionen? Wechseln Sie in den Expertenmodus für das vollständige Formular.',
  },
  'es-ES': {
    subtitle: 'Emitir una clave con dos campos; el inquilino y la aplicación se rellenan automáticamente con valores predeterminados',
    clientName: 'Nombre del cliente',
    clientNamePlaceholder: 'por ejemplo, prod, dev, zhangsan-cli',
    clientNameHint: 'Identificador único bajo el mismo inquilino + aplicación',
    clientNameRequired: 'El nombre del cliente es obligatorio',
    remark: 'Comentario',
    remarkPlaceholder: 'Explique el propósito, por ejemplo, para pruebas, producción',
    sign: 'Emitir',
    signing: 'Emitiendo...',
    created: 'Clave creada',
    copyHint: 'Por favor, copie y guarde inmediatamente; la clave completa no se mostrará de nuevo después de cerrar',
    client: 'Cliente',
    signAnother: 'Emitir otra',
    backToList: 'Volver a la lista de acceso',
    tenant: 'Inquilino',
    application: 'Aplicación',
    autoFillHint: 'Los siguientes campos se rellenan automáticamente con valores predeterminados:',
    advancedHint: '¿Necesita más opciones? Cambie al modo experto para el formulario completo.',
  },
  'fr-FR': {
    subtitle: 'Émettre une clé avec deux champs; le locataire et l\'application sont remplis automatiquement avec les valeurs par défaut',
    clientName: 'Nom du client',
    clientNamePlaceholder: 'par ex. prod, dev, zhangsan-cli',
    clientNameHint: 'Identifiant unique sous le même locataire + application',
    clientNameRequired: 'Le nom du client est obligatoire',
    remark: 'Remarque',
    remarkPlaceholder: 'Expliquer l\'objectif, par ex. pour les tests, la production',
    sign: 'Émettre',
    signing: 'Émission en cours...',
    created: 'Clé créée',
    copyHint: 'Veuillez copier et enregistrer immédiatement; la clé complète ne sera plus affichée après fermeture',
    client: 'Client',
    signAnother: 'Émettre une autre',
    backToList: 'Retour à la liste d\'accès',
    tenant: 'Locataire',
    application: 'Application',
    autoFillHint: 'Les champs suivants sont remplis automatiquement avec les valeurs par défaut:',
    advancedHint: 'Besoin de plus d\'options? Passez en mode expert pour le formulaire complet.',
  },
  'ja-JP': {
    subtitle: '2つのフィールドでキーを発行；テナントとアプリケーションはデフォルト値で自動入力されます',
    clientName: 'クライアント名',
    clientNamePlaceholder: '例: prod、dev、zhangsan-cli',
    clientNameHint: '同じテナント+アプリケーション下での一意の識別子',
    clientNameRequired: 'クライアント名は必須です',
    remark: '備考',
    remarkPlaceholder: '目的を説明してください、例：テスト用、本番環境',
    sign: '発行',
    signing: '発行中...',
    created: 'キーが作成されました',
    copyHint: 'すぐにコピーして保存してください；閉じた後、完全なキーは再表示されません',
    client: 'クライアント',
    signAnother: 'もう一つ発行',
    backToList: 'アクセスリストに戻る',
    tenant: 'テナント',
    application: 'アプリケーション',
    autoFillHint: '以下のフィールドはデフォルト値で自動入力されます：',
    advancedHint: 'より多くのオプションが必要ですか？完全なフォームのためにエキスパートモードに切り替えてください。',
  },
  'zh-TW': {
    subtitle: '兩個欄位簽發一個金鑰，租戶與應用程式自動填入預設值',
    clientName: '客戶端名稱',
    clientNamePlaceholder: '例如：prod、dev、zhangsan-cli',
    clientNameHint: '同一租戶+應用程式下的唯一識別碼',
    clientNameRequired: '客戶端名稱必填',
    remark: '備註',
    remarkPlaceholder: '說明建立原因，例如：測試使用、正式環境',
    sign: '簽發',
    signing: '簽發中...',
    created: '金鑰已簽發',
    copyHint: '請立即複製儲存，關閉後將無法再次檢視完整金鑰',
    client: '客戶端',
    signAnother: '再簽一個',
    backToList: '返回接入清單',
    tenant: '租戶',
    application: '應用程式',
    autoFillHint: '以下欄位自動填入預設值：',
    advancedHint: '需要更多設定？切換到專家模式使用完整簽發表單。',
  },
}

for (const [lang, keys] of Object.entries(translations)) {
  const filePath = join(localesDir, lang, 'simpleMode.ts')
  let content = readFileSync(filePath, 'utf-8')
  
  // 检查是否已经有 keys 部分
  if (content.includes('keys:')) {
    console.log(`${lang}: keys section already exists, skipping`)
    continue
  }
  
  // 在最后一个 } 前插入 keys 部分
  const keysSection = `  keys: {
    subtitle: '${keys.subtitle}',
    clientName: '${keys.clientName}',
    clientNamePlaceholder: '${keys.clientNamePlaceholder}',
    clientNameHint: '${keys.clientNameHint}',
    clientNameRequired: '${keys.clientNameRequired}',
    remark: '${keys.remark}',
    remarkPlaceholder: '${keys.remarkPlaceholder}',
    sign: '${keys.sign}',
    signing: '${keys.signing}',
    created: '${keys.created}',
    copyHint: '${keys.copyHint}',
    client: '${keys.client}',
    signAnother: '${keys.signAnother}',
    backToList: '${keys.backToList}',
    tenant: '${keys.tenant}',
    application: '${keys.application}',
    autoFillHint: '${keys.autoFillHint}',
    advancedHint: '${keys.advancedHint}',
  },
}`
  
  // 替换最后的 }
  content = content.replace(/(\s+)\}\s*$/, `$1},\n${keysSection}\n}`)
  
  writeFileSync(filePath, content, 'utf-8')
  console.log(`${lang}: keys section added`)
}

console.log('Done')
