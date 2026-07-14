#!/usr/bin/env node
/**
 * Sync usageCost, sessions.compare/replay keys, and approval.config to 6 locales.
 */
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const localesDir = path.join(__dirname, '../src/locales')
const TARGETS = ['de-DE', 'fr-FR', 'es-ES', 'ar-SA', 'ja-JP', 'zh-TW']

function formatValue(v, indent) {
  if (typeof v === 'string') {
    const q = v.includes("'") ? '"' : "'"
    return `${q}${v}${q}`
  }
  if (typeof v === 'object' && v !== null) {
    const inner = Object.entries(v)
      .map(([k, val]) => `${' '.repeat(indent + 2)}${k}: ${formatValue(val, indent + 2)},`)
      .join('\n')
    return `{\n${inner}\n${' '.repeat(indent)}}`
  }
  return String(v)
}

function formatBlock(name, obj, indent = 2) {
  const pad = ' '.repeat(indent)
  const lines = Object.entries(obj).map(
    ([k, v]) => `${pad}  ${k}: ${formatValue(v, indent + 2)},`,
  )
  return `${pad}${name}: {\n${lines.join('\n')}\n${pad}},`
}

const usageCostByLocale = {
  'de-DE': {
    refresh: 'Aktualisieren',
    loading: 'Wird geladen…',
    compare: {
      title: 'Kostenvergleich',
      currentPeriod: 'Aktuell:',
      previousPeriod: 'Vergleich:',
      currentLabel: 'Aktuell ({period})',
      previousLabel: 'Vergleichszeitraum ({period})',
      change: 'Änderung',
      significant: 'Signifikant',
      meta: '{requests} Anfragen · {models} Modelle',
    },
    attribution: {
      title: 'Kostenzuordnung',
      groupBy: 'Gruppieren nach:',
      model: 'Nach Modell',
      provider: 'Nach Anbieter',
      intent: 'Nach Intent',
      shareChart: 'Kostenanteil',
      detailChart: 'Kostenaufschlüsselung (Input vs Output)',
      otherNote: 'Weitere {count} Einträge gesamt: {cost}',
      colModel: 'Modell',
      colProvider: 'Anbieter',
      colIntent: 'Intent',
      colRequests: 'Anfragen',
      colTotalCost: 'Gesamtkosten',
      colInputCost: 'Input-Kosten',
      colOutputCost: 'Output-Kosten',
      colShare: 'Anteil',
      colLatency: 'Ø Latenz',
    },
    charts: { inputCost: 'Input-Kosten', outputCost: 'Output-Kosten' },
    cache: {
      title: 'Cache-Ökonomie',
      hitRate: 'Cache-Trefferquote',
      cacheSaved: 'Cache-Einsparung',
      cacheSavedHint: 'vs. ohne Cache',
      compressionSaved: 'Kompressions-Einsparung',
      compressionCount: '{n} Kompressionen',
      savingsRate: 'Gesamteinsparungsrate',
      totalSaved: 'Gespart {cost}',
      actualSpend: 'Tatsächliche Ausgaben',
      effectiveRatio: '{pct} der unk optimierten Kosten',
      totalRequests: 'Anfragen gesamt',
    },
    errors: {
      costTrend: 'Kostentrend konnte nicht geladen werden',
      periodCompare: 'Periodenvergleich konnte nicht geladen werden',
      cacheEconomics: 'Cache-Ökonomie konnte nicht geladen werden',
    },
  },
  'fr-FR': {
    refresh: 'Actualiser',
    loading: 'Chargement…',
    compare: {
      title: 'Comparaison des coûts',
      currentPeriod: 'Période actuelle :',
      previousPeriod: 'Comparer :',
      currentLabel: 'Actuelle ({period})',
      previousLabel: 'Période de référence ({period})',
      change: 'Variation',
      significant: 'Significatif',
      meta: '{requests} requêtes · {models} modèles',
    },
    attribution: {
      title: 'Attribution des coûts',
      groupBy: 'Grouper par :',
      model: 'Par modèle',
      provider: 'Par fournisseur',
      intent: 'Par intention',
      shareChart: 'Part des coûts',
      detailChart: 'Détail (entrée vs sortie)',
      otherNote: 'Autres {count} éléments : {cost}',
      colModel: 'Modèle',
      colProvider: 'Fournisseur',
      colIntent: 'Intention',
      colRequests: 'Requêtes',
      colTotalCost: 'Coût total',
      colInputCost: 'Coût entrée',
      colOutputCost: 'Coût sortie',
      colShare: 'Part',
      colLatency: 'Latence moy.',
    },
    charts: { inputCost: 'Coût entrée', outputCost: 'Coût sortie' },
    cache: {
      title: 'Économie du cache',
      hitRate: 'Taux de succès cache',
      cacheSaved: 'Économies cache',
      cacheSavedHint: 'vs sans cache',
      compressionSaved: 'Économies compression',
      compressionCount: '{n} compressions',
      savingsRate: 'Taux d’économie global',
      totalSaved: 'Total économisé {cost}',
      actualSpend: 'Dépenses réelles',
      effectiveRatio: '{pct} du coût non optimisé',
      totalRequests: 'Requêtes totales',
    },
    errors: {
      costTrend: 'Échec du chargement de la tendance des coûts',
      periodCompare: 'Échec du chargement de la comparaison',
      cacheEconomics: 'Échec du chargement de l’économie cache',
    },
  },
  'es-ES': {
    refresh: 'Actualizar',
    loading: 'Cargando…',
    compare: {
      title: 'Comparación de costes',
      currentPeriod: 'Periodo actual:',
      previousPeriod: 'Comparar:',
      currentLabel: 'Actual ({period})',
      previousLabel: 'Periodo anterior ({period})',
      change: 'Cambio',
      significant: 'Significativo',
      meta: '{requests} solicitudes · {models} modelos',
    },
    attribution: {
      title: 'Atribución de costes',
      groupBy: 'Agrupar por:',
      model: 'Por modelo',
      provider: 'Por proveedor',
      intent: 'Por intención',
      shareChart: 'Participación de coste',
      detailChart: 'Desglose (entrada vs salida)',
      otherNote: 'Otros {count} elementos: {cost}',
      colModel: 'Modelo',
      colProvider: 'Proveedor',
      colIntent: 'Intención',
      colRequests: 'Solicitudes',
      colTotalCost: 'Coste total',
      colInputCost: 'Coste entrada',
      colOutputCost: 'Coste salida',
      colShare: 'Participación',
      colLatency: 'Latencia media',
    },
    charts: { inputCost: 'Coste entrada', outputCost: 'Coste salida' },
    cache: {
      title: 'Economía de caché',
      hitRate: 'Tasa de aciertos',
      cacheSaved: 'Ahorro por caché',
      cacheSavedHint: 'vs sin caché',
      compressionSaved: 'Ahorro por compresión',
      compressionCount: '{n} compresiones',
      savingsRate: 'Tasa de ahorro global',
      totalSaved: 'Ahorro total {cost}',
      actualSpend: 'Gasto real',
      effectiveRatio: '{pct} del coste sin optimizar',
      totalRequests: 'Solicitudes totales',
    },
    errors: {
      costTrend: 'Error al cargar tendencia de costes',
      periodCompare: 'Error al cargar comparación de periodos',
      cacheEconomics: 'Error al cargar economía de caché',
    },
  },
  'ar-SA': {
    refresh: 'تحديث',
    loading: 'جاري التحميل…',
    compare: {
      title: 'مقارنة التكلفة',
      currentPeriod: 'الفترة الحالية:',
      previousPeriod: 'المقارنة:',
      currentLabel: 'الحالية ({period})',
      previousLabel: 'فترة المقارنة ({period})',
      change: 'التغيير',
      significant: 'ملحوظ',
      meta: '{requests} طلب · {models} نماذج',
    },
    attribution: {
      title: 'إسناد التكلفة',
      groupBy: 'التجميع حسب:',
      model: 'حسب النموذج',
      provider: 'حسب المزود',
      intent: 'حسب النية',
      shareChart: 'حصة التكلفة',
      detailChart: 'تفصيل التكلفة (إدخال مقابل إخراج)',
      otherNote: 'عناصر أخرى {count}: {cost}',
      colModel: 'النموذج',
      colProvider: 'المزود',
      colIntent: 'النية',
      colRequests: 'الطلبات',
      colTotalCost: 'التكلفة الإجمالية',
      colInputCost: 'تكلفة الإدخال',
      colOutputCost: 'تكلفة الإخراج',
      colShare: 'الحصة',
      colLatency: 'متوسط التأخير',
    },
    charts: { inputCost: 'تكلفة الإدخال', outputCost: 'تكلفة الإخراج' },
    cache: {
      title: 'اقتصاديات التخزين المؤقت',
      hitRate: 'معدل إصابة التخزين المؤقت',
      cacheSaved: 'توفير التخزين المؤقت',
      cacheSavedHint: 'مقارنة بلا تخزين مؤقت',
      compressionSaved: 'توفير الضغط',
      compressionCount: '{n} عمليات ضغط',
      savingsRate: 'معدل التوفير الإجمالي',
      totalSaved: 'إجمالي التوفير {cost}',
      actualSpend: 'الإنفاق الفعلي',
      effectiveRatio: '{pct} من التكلفة غير المحسّنة',
      totalRequests: 'إجمالي الطلبات',
    },
    errors: {
      costTrend: 'فشل تحميل اتجاه التكلفة',
      periodCompare: 'فشل تحميل مقارنة الفترات',
      cacheEconomics: 'فشل تحميل اقتصاديات التخزين المؤقت',
    },
  },
  'ja-JP': {
    refresh: '更新',
    loading: '読み込み中…',
    compare: {
      title: 'コスト比較',
      currentPeriod: '当期:',
      previousPeriod: '比較:',
      currentLabel: '当期 ({period})',
      previousLabel: '比較期 ({period})',
      change: '変化',
      significant: '有意',
      meta: '{requests} リクエスト · {models} モデル',
    },
    attribution: {
      title: 'コスト帰属',
      groupBy: 'グループ化:',
      model: 'モデル別',
      provider: 'プロバイダ別',
      intent: 'インテント別',
      shareChart: 'コスト比率',
      detailChart: 'コスト内訳（入力 vs 出力）',
      otherNote: 'その他 {count} 件合計: {cost}',
      colModel: 'モデル',
      colProvider: 'プロバイダ',
      colIntent: 'インテント',
      colRequests: 'リクエスト数',
      colTotalCost: '総コスト',
      colInputCost: '入力コスト',
      colOutputCost: '出力コスト',
      colShare: '比率',
      colLatency: '平均レイテンシ',
    },
    charts: { inputCost: '入力コスト', outputCost: '出力コスト' },
    cache: {
      title: 'キャッシュ経済',
      hitRate: 'キャッシュヒット率',
      cacheSaved: 'キャッシュ節約',
      cacheSavedHint: 'キャッシュなし比',
      compressionSaved: '圧縮節約',
      compressionCount: '{n} 回圧縮',
      savingsRate: '総合節約率',
      totalSaved: '総節約 {cost}',
      actualSpend: '実支出',
      effectiveRatio: '未最適化コストの {pct}',
      totalRequests: '総リクエスト数',
    },
    errors: {
      costTrend: 'コスト推移の読み込みに失敗',
      periodCompare: '期間比較の読み込みに失敗',
      cacheEconomics: 'キャッシュ経済の読み込みに失敗',
    },
  },
  'zh-TW': {
    refresh: '重新整理',
    loading: '載入中…',
    compare: {
      title: '成本對比',
      currentPeriod: '本期：',
      previousPeriod: '對比：',
      currentLabel: '本期 ({period})',
      previousLabel: '對比期 ({period})',
      change: '變化',
      significant: '顯著',
      meta: '{requests} 次請求 · {models} 個模型',
    },
    attribution: {
      title: '成本歸因',
      groupBy: '分組維度：',
      model: '按模型',
      provider: '按提供商',
      intent: '按意圖',
      shareChart: '成本佔比',
      detailChart: '成本明細（輸入 vs 輸出）',
      otherNote: '其他 {count} 項合計: {cost}',
      colModel: '模型',
      colProvider: '提供商',
      colIntent: '意圖',
      colRequests: '請求數',
      colTotalCost: '總成本',
      colInputCost: '輸入成本',
      colOutputCost: '輸出成本',
      colShare: '佔比',
      colLatency: '平均延遲',
    },
    charts: { inputCost: '輸入成本', outputCost: '輸出成本' },
    cache: {
      title: '快取經濟學',
      hitRate: '快取命中率',
      cacheSaved: '快取節省',
      cacheSavedHint: '相對無快取',
      compressionSaved: '壓縮節省',
      compressionCount: '{n} 次壓縮',
      savingsRate: '綜合節省率',
      totalSaved: '總節省 {cost}',
      actualSpend: '實際支出',
      effectiveRatio: '占無優化成本 {pct}',
      totalRequests: '總請求數',
    },
    errors: {
      costTrend: '載入成本趨勢失敗',
      periodCompare: '載入期間對比失敗',
      cacheEconomics: '載入快取經濟學失敗',
    },
  },
}

const comparePanelsExtra = {
  'de-DE': { msgCountSuffix: '{n} Nachrichten', notCompressedBadge: 'Nicht komprimiert' },
  'fr-FR': { msgCountSuffix: '{n} messages', notCompressedBadge: 'Non compressé' },
  'es-ES': { msgCountSuffix: '{n} mensajes', notCompressedBadge: 'Sin comprimir' },
  'ar-SA': { msgCountSuffix: '{n} رسائل', notCompressedBadge: 'غير مضغوط' },
  'ja-JP': { msgCountSuffix: '{n} 件', notCompressedBadge: '未圧縮' },
  'zh-TW': { msgCountSuffix: '{n} 則', notCompressedBadge: '未壓縮' },
}

const replayExtra = {
  'de-DE': {
    summary: { titleLabel: 'Titel:', summaryLabel: 'Zusammenfassung:', topicsLabel: 'Themen:', empty: '(leer)' },
    stepTable: { strategy: 'Kompressionsstrategie', lossiness: 'Informationsverlust', cacheTier: 'Cache-Ebene', bytesIn: 'In (B)', bytesOut: 'Out (B)', deltaMsg: 'Δ Nachrichten', windowTrigger: 'Fenster-Trigger', summaryMarker: 'Zusammenfassungsmarker' },
    loadFailed: 'Laden fehlgeschlagen',
  },
  'fr-FR': {
    summary: { titleLabel: 'Titre :', summaryLabel: 'Résumé :', topicsLabel: 'Sujets :', empty: '(vide)' },
    stepTable: { strategy: 'Stratégie de compression', lossiness: 'Perte d’information', cacheTier: 'Niveau cache', bytesIn: 'In (o)', bytesOut: 'Out (o)', deltaMsg: 'Δ messages', windowTrigger: 'Déclencheur fenêtre', summaryMarker: 'Marqueur résumé' },
    loadFailed: 'Échec du chargement',
  },
  'es-ES': {
    summary: { titleLabel: 'Título:', summaryLabel: 'Resumen:', topicsLabel: 'Temas:', empty: '(vacío)' },
    stepTable: { strategy: 'Estrategia de compresión', lossiness: 'Pérdida de información', cacheTier: 'Nivel de caché', bytesIn: 'In (B)', bytesOut: 'Out (B)', deltaMsg: 'Δ mensajes', windowTrigger: 'Disparador ventana', summaryMarker: 'Marcador resumen' },
    loadFailed: 'Error al cargar',
  },
  'ar-SA': {
    summary: { titleLabel: 'العنوان:', summaryLabel: 'الملخص:', topicsLabel: 'المواضيع:', empty: '(فارغ)' },
    stepTable: { strategy: 'استراتيجية الضغط', lossiness: 'فقدان المعلومات', cacheTier: 'طبقة التخزين', bytesIn: 'In (ب)', bytesOut: 'Out (ب)', deltaMsg: 'Δ رسائل', windowTrigger: 'محفّز النافذة', summaryMarker: 'علامة الملخص' },
    loadFailed: 'فشل التحميل',
  },
  'ja-JP': {
    summary: { titleLabel: 'タイトル:', summaryLabel: '要約:', topicsLabel: 'トピック:', empty: '（空）' },
    stepTable: { strategy: '圧縮戦略', lossiness: '情報損失', cacheTier: 'キャッシュ層', bytesIn: 'In (B)', bytesOut: 'Out (B)', deltaMsg: 'Δ メッセージ', windowTrigger: 'ウィンドウトリガー', summaryMarker: '要約マーカー' },
    loadFailed: '読み込みに失敗',
  },
  'zh-TW': {
    summary: { titleLabel: '標題：', summaryLabel: '摘要：', topicsLabel: '主題：', empty: '（空）' },
    stepTable: { strategy: '壓縮策略', lossiness: 'Lossiness', cacheTier: '快取層', bytesIn: 'In (B)', bytesOut: 'Out (B)', deltaMsg: 'Δ 訊息', windowTrigger: '視窗觸發', summaryMarker: '摘要標記' },
    loadFailed: '載入失敗',
  },
}

const approvalConfigByLocale = {
  'de-DE': {
    title: 'Genehmigungseinstellungen',
    sections: { basic: 'Grundeinstellungen', approvers: 'Genehmiger', channels: 'Benachrichtigungskanäle', rules: 'Genehmigungsregeln' },
    save: 'Einstellungen speichern', saving: 'Wird gespeichert…', loading: 'Wird geladen…',
    description: 'Genehmigungsablauf, Genehmiger und Benachrichtigungskanäle konfigurieren',
    enabled: { label: 'Genehmigungsablauf aktivieren', hint: 'Passende Anfragen werden in die Warteschlange gestellt', on: 'Aktiviert', off: 'Deaktiviert' },
    mode: { label: 'Genehmigungsmodus', hint: 'Wie Genehmigungen verarbeitet werden', disabled: 'Deaktiviert', disabledDesc: 'Genehmigungen vollständig aus', automatic: 'Automatisch', automaticDesc: 'Automatisch nach Regeln', manual: 'Manuell', manualDesc: 'Manuelle Genehmigung erforderlich' },
    timeout: { label: 'Genehmigungs-Timeout', hint: 'Aktuell: {value}', suffix: 'Sek.' },
    timeoutAction: { label: 'Bei Timeout', hint: 'Verhalten bei Zeitüberschreitung', approve: 'Auto-genehmigen', approveDesc: 'Anfrage automatisch genehmigen', reject: 'Auto-ablehnen', rejectDesc: 'Anfrage automatisch ablehnen' },
    sectionsDesc: { approvers: 'Genehmiger und Priorität konfigurieren', channels: 'Benachrichtigungskanäle konfigurieren', rules: 'Definieren, welche Anfragen genehmigt werden müssen' },
    errors: { loadFailed: 'Einstellungen konnten nicht geladen werden', saveFailed: 'Speichern fehlgeschlagen' },
    success: { saved: 'Erfolgreich gespeichert' },
    format: { seconds: '{n} Sek.', minutes: '{n} Min.', hours: '{n} Std.' },
  },
  'fr-FR': {
    title: 'Paramètres d’approbation',
    sections: { basic: 'Paramètres de base', approvers: 'Approbateurs', channels: 'Canaux de notification', rules: 'Règles d’approbation' },
    save: 'Enregistrer', saving: 'Enregistrement…', loading: 'Chargement…',
    description: 'Configurer le flux d’approbation, les approbateurs et les canaux',
    enabled: { label: 'Activer le flux d’approbation', hint: 'Les requêtes correspondantes entrent dans la file', on: 'Activé', off: 'Désactivé' },
    mode: { label: 'Mode d’approbation', hint: 'Comment les approbations sont traitées', disabled: 'Désactivé', disabledDesc: 'Désactiver complètement', automatic: 'Automatique', automaticDesc: 'Traitement par règles', manual: 'Manuel', manualDesc: 'Approbation humaine requise' },
    timeout: { label: 'Délai d’approbation', hint: 'Actuel : {value}', suffix: 's' },
    timeoutAction: { label: 'En cas de dépassement', hint: 'Comportement après expiration', approve: 'Approuver auto.', approveDesc: 'Approuver automatiquement', reject: 'Rejeter auto.', rejectDesc: 'Rejeter automatiquement' },
    sectionsDesc: { approvers: 'Configurer les approbateurs et priorités', channels: 'Configurer les canaux de notification', rules: 'Définir les requêtes à approuver' },
    errors: { loadFailed: 'Échec du chargement des paramètres', saveFailed: 'Échec de l’enregistrement' },
    success: { saved: 'Enregistré avec succès' },
    format: { seconds: '{n} s', minutes: '{n} min', hours: '{n} h' },
  },
  'es-ES': {
    title: 'Configuración de aprobación',
    sections: { basic: 'Ajustes básicos', approvers: 'Aprobadores', channels: 'Canales de notificación', rules: 'Reglas de aprobación' },
    save: 'Guardar ajustes', saving: 'Guardando…', loading: 'Cargando…',
    description: 'Configurar flujo de aprobación, aprobadores y canales',
    enabled: { label: 'Activar flujo de aprobación', hint: 'Las solicitudes coincidentes entran en cola', on: 'Activado', off: 'Desactivado' },
    mode: { label: 'Modo de aprobación', hint: 'Cómo se procesan las aprobaciones', disabled: 'Desactivado', disabledDesc: 'Desactivar por completo', automatic: 'Automático', automaticDesc: 'Procesar por reglas', manual: 'Manual', manualDesc: 'Requiere aprobación humana' },
    timeout: { label: 'Tiempo de espera', hint: 'Actual: {value}', suffix: 's' },
    timeoutAction: { label: 'Al expirar', hint: 'Qué ocurre al agotar el tiempo', approve: 'Aprobar auto.', approveDesc: 'Aprobar automáticamente', reject: 'Rechazar auto.', rejectDesc: 'Rechazar automáticamente' },
    sectionsDesc: { approvers: 'Configurar aprobadores y prioridad', channels: 'Configurar canales de notificación', rules: 'Definir qué solicitudes requieren aprobación' },
    errors: { loadFailed: 'Error al cargar la configuración', saveFailed: 'Error al guardar' },
    success: { saved: 'Guardado correctamente' },
    format: { seconds: '{n} s', minutes: '{n} min', hours: '{n} h' },
  },
  'ar-SA': {
    title: 'إعدادات الموافقة',
    sections: { basic: 'الإعدادات الأساسية', approvers: 'الموافقون', channels: 'قنوات الإشعار', rules: 'قواعد الموافقة' },
    save: 'حفظ الإعدادات', saving: 'جاري الحفظ…', loading: 'جاري التحميل…',
    description: 'تكوين سير الموافقة والموافقين وقنوات الإشعار',
    enabled: { label: 'تفعيل سير الموافقة', hint: 'الطلبات المطابقة تدخل قائمة الانتظار', on: 'مفعّل', off: 'معطّل' },
    mode: { label: 'وضع الموافقة', hint: 'كيفية معالجة الموافقات', disabled: 'معطّل', disabledDesc: 'إيقاف الموافقات تمامًا', automatic: 'تلقائي', automaticDesc: 'المعالجة حسب القواعد', manual: 'يدوي', manualDesc: 'يتطلب موافقة بشرية' },
    timeout: { label: 'مهلة الموافقة', hint: 'الحالي: {value}', suffix: 'ث' },
    timeoutAction: { label: 'عند انتهاء المهلة', hint: 'ماذا يحدث عند انتهاء الوقت', approve: 'موافقة تلقائية', approveDesc: 'الموافقة تلقائيًا', reject: 'رفض تلقائي', rejectDesc: 'الرفض تلقائيًا' },
    sectionsDesc: { approvers: 'تكوين الموافقين والأولوية', channels: 'تكوين قنوات الإشعار', rules: 'تحديد الطلبات التي تحتاج موافقة' },
    errors: { loadFailed: 'فشل تحميل الإعدادات', saveFailed: 'فشل الحفظ' },
    success: { saved: 'تم الحفظ بنجاح' },
    format: { seconds: '{n} ث', minutes: '{n} د', hours: '{n} س' },
  },
  'ja-JP': {
    title: '承認設定',
    sections: { basic: '基本設定', approvers: '承認者', channels: '通知チャネル', rules: '承認ルール' },
    save: '設定を保存', saving: '保存中…', loading: '読み込み中…',
    description: '承認フロー、承認者、通知チャネルを設定',
    enabled: { label: '承認フローを有効化', hint: '条件に合うリクエストは承認キューに入ります', on: '有効', off: '無効' },
    mode: { label: '承認モード', hint: '承認の処理方法', disabled: '無効', disabledDesc: '承認を完全にオフ', automatic: '自動', automaticDesc: 'ルールで自動処理', manual: '手動', manualDesc: '人手による承認が必要' },
    timeout: { label: '承認タイムアウト', hint: '現在: {value}', suffix: '秒' },
    timeoutAction: { label: 'タイムアウト時', hint: '期限切れ時の動作', approve: '自動承認', approveDesc: 'リクエストを自動承認', reject: '自動拒否', rejectDesc: 'リクエストを自動拒否' },
    sectionsDesc: { approvers: '承認者と優先度を設定', channels: '通知チャネルを設定', rules: '承認が必要なリクエストを定義' },
    errors: { loadFailed: '設定の読み込みに失敗', saveFailed: '保存に失敗' },
    success: { saved: '保存しました' },
    format: { seconds: '{n} 秒', minutes: '{n} 分', hours: '{n} 時間' },
  },
  'zh-TW': {
    title: '審批設定',
    sections: { basic: '基本設定', approvers: '審批人管理', channels: '通知渠道', rules: '審批規則' },
    save: '儲存設定', saving: '儲存中…', loading: '載入中…',
    description: '設定審批流程、審批人與通知渠道',
    enabled: { label: '啟用審批流程', hint: '開啟後，符合規則的請求將進入審批流程', on: '已啟用', off: '已停用' },
    mode: { label: '審批模式', hint: '選擇審批的工作模式', disabled: '停用', disabledDesc: '完全關閉審批功能', automatic: '自動審批', automaticDesc: '根據規則自動處理', manual: '人工審批', manualDesc: '需要審批人手動審批' },
    timeout: { label: '審批逾時時間', hint: '目前設定: {value}', suffix: '秒' },
    timeoutAction: { label: '逾時後行為', hint: '審批逾時後的處理方式', approve: '自動通過', approveDesc: '逾時後自動批准請求', reject: '自動拒絕', rejectDesc: '逾時後自動拒絕請求' },
    sectionsDesc: { approvers: '設定審批人員及其優先順序', channels: '設定審批通知的發送渠道', rules: '定義哪些請求需要審批' },
    errors: { loadFailed: '載入設定失敗', saveFailed: '儲存失敗' },
    success: { saved: '儲存成功' },
    format: { seconds: '{n} 秒', minutes: '{n} 分鐘', hours: '{n} 小時' },
  },
}

for (const locale of TARGETS) {
  // ── dataLifecycle.usageCost ──
  const dlPath = path.join(localesDir, locale, 'dataLifecycle.ts')
  let dl = fs.readFileSync(dlPath, 'utf8')
  if (!dl.includes('\n  usageCost:')) {
    const block = formatBlock('usageCost', usageCostByLocale[locale])
    dl = dl.replace(/(\n  pages: \{[\s\S]*?\n  \},)\n(\})/, `$1\n\n${block}\n$2`)
    fs.writeFileSync(dlPath, dl, 'utf8')
    console.log(`+ ${locale}/dataLifecycle.ts usageCost`)
  }

  // ── sessions.compare panels + replay extras ──
  const sesPath = path.join(localesDir, locale, 'sessions.ts')
  let ses = fs.readFileSync(sesPath, 'utf8')
  if (!ses.includes('msgCountSuffix')) {
    const extra = comparePanelsExtra[locale]
    ses = ses.replace(
      /(cacheSavings: [^\n]+,)\n(\s+empty:)/,
      `$1\n      msgCountSuffix: ${formatValue(extra.msgCountSuffix, 6)},\n      notCompressedBadge: ${formatValue(extra.notCompressedBadge, 6)},\n$2`,
    )
    console.log(`+ ${locale}/sessions.ts compare panels`)
  }
  if (!ses.includes('summary: {') || !ses.match(/replay:[\s\S]*summary:/)) {
    const extra = replayExtra[locale]
    const replayTail = [
      `    summary: ${formatValue(extra.summary, 4)},`,
      `    stepTable: ${formatValue(extra.stepTable, 4)},`,
      `    loadFailed: ${formatValue(extra.loadFailed, 4)},`,
    ].join('\n')
    ses = ses.replace(
      /(avgRatio: [^\n]+,)\n(  \},\n  contextLayout:)/,
      `$1\n${replayTail}\n$2`,
    )
    console.log(`+ ${locale}/sessions.ts replay extras`)
  }
  fs.writeFileSync(sesPath, ses, 'utf8')

  // ── approval.config ──
  const apPath = path.join(localesDir, locale, 'approval.ts')
  let ap = fs.readFileSync(apPath, 'utf8')
  if (!ap.includes('sectionsDesc:')) {
    const block = formatBlock('config', approvalConfigByLocale[locale], 2).replace(/,$/, '')
    ap = ap.replace(/  config: \{[\s\S]*?\n  \},\n\}/, `${block}\n}`)
    fs.writeFileSync(apPath, ap, 'utf8')
    console.log(`+ ${locale}/approval.ts config`)
  }
}

// ── zh-CN / en-US usageCost.errors ──
for (const locale of ['zh-CN', 'en-US']) {
  const dlPath = path.join(localesDir, locale, 'dataLifecycle.ts')
  let dl = fs.readFileSync(dlPath, 'utf8')
  if (!dl.includes('errors:')) {
    const errors =
      locale === 'zh-CN'
        ? {
            costTrend: '加载成本趋势失败',
            periodCompare: '加载周期对比失败',
            cacheEconomics: '加载缓存经济学失败',
          }
        : {
            costTrend: 'Failed to fetch cost trend',
            periodCompare: 'Failed to fetch period comparison',
            cacheEconomics: 'Failed to fetch cache economics',
          }
    dl = dl.replace(
      /(totalRequests: [^\n]+,)\n(    \},\n  \},)/,
      `$1\n    },\n    errors: ${formatValue(errors, 4)},\n  },`,
    )
    fs.writeFileSync(dlPath, dl, 'utf8')
    console.log(`+ ${locale}/dataLifecycle.ts usageCost.errors`)
  }
}

console.log('done')
