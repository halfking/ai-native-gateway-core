#!/usr/bin/env node
/**
 * Replace Chinese sessions.management / sessions.turns blocks in 6 locales.
 * Also fixes stats.retry and corrupt compressionTip tails.
 */
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const localesDir = path.join(__dirname, '../src/locales')
const TARGETS = ['de-DE', 'fr-FR', 'es-ES', 'ar-SA', 'ja-JP', 'zh-TW']

function formatValue(v, indent) {
  if (typeof v === 'string') {
    const escaped = v.replace(/\\/g, '\\\\').replace(/'/g, "\\'")
    return `'${escaped}'`
  }
  if (typeof v === 'object' && v !== null) {
    const inner = Object.entries(v)
      .map(([k, val]) => `${' '.repeat(indent + 2)}${k}: ${formatValue(val, indent + 2)},`)
      .join('\n')
    return `{\n${inner}\n${' '.repeat(indent)}}`
  }
  return String(v)
}

function formatSection(name, obj) {
  const lines = Object.entries(obj).map(
    ([k, v]) => `    ${k}: ${formatValue(v, 4)},`,
  )
  return `  ${name}: {\n${lines.join('\n')}\n  },`
}

const managementByLocale = {
  'de-DE': {
    title: 'Sitzungsverwaltung',
    refresh: 'Aktualisieren',
    loading: 'Wird geladen…',
    columns: {
      status: 'Status', title: 'Titel', sessionId: 'Sitzungs-ID', tenant: 'Mandant',
      turns: 'Runden', cost: 'Kosten', tokens: 'Tokens', model: 'Aktuelles Modell',
      healthGrade: 'Gesundheitsnote', lastActive: 'Zuletzt aktiv', actions: 'Aktionen',
    },
    detail: {
      title: 'Sitzungsdetails', status: 'Status', healthGrade: 'Gesundheitsnote', tenant: 'Mandant',
      apiKeyId: 'API-Key-ID', totalTurns: 'Gesamtrunden', promptTokens: 'Prompt-Tokens',
      completionTokens: 'Completion-Tokens', totalCost: 'Gesamtkosten', currentCredential: 'Aktuelle Anmeldedaten',
      currentModel: 'Aktuelles Modell', titleLabel: 'Titel', annotation: 'Anmerkung', tags: 'Tags',
      rotationHistory: 'Anmeldedaten-Rotation ({count})', ongoing: 'laufend',
    },
    actions: { detail: 'Details', stop: 'Stoppen', recover: 'Wiederherstellen', close: 'Schließen' },
    states: {
      active: 'Aktiv', stopped: 'Gestoppt', recovered: 'Wiederhergestellt', waiting: 'Wartend',
      error: 'Fehler', expired: 'Abgelaufen', zombie: '⚠ Zombie',
    },
    healthGrades: { A: 'Ausgezeichnet', B: 'Gut', C: 'Durchschnittlich', D: 'Schwach', F: 'Kritisch' },
    confirm: { stopTitle: 'Sitzung stoppen', stopMessage: 'Diese Sitzung wirklich stoppen?' },
    errors: {
      loadFailed: 'Sitzungen laden fehlgeschlagen: {msg}',
      stopFailed: 'Sitzung stoppen fehlgeschlagen: {msg}',
      recoverFailed: 'Wiederherstellung fehlgeschlagen: {msg}',
      sseParseFailed: 'SSE-Ereignis konnte nicht geparst werden',
      serviceUnavailable: 'Sitzungsdienst nicht verfügbar (HTTP 503). Später erneut versuchen.',
      httpStatus: 'Anfrage fehlgeschlagen (HTTP {status})',
    },
    empty: 'Keine Sitzungen',
    emptyHint: 'Keine aktiven Sitzungen im aktuellen Zeitfenster. Neue Sitzungen erscheinen nach gw_session_id in Request-Logs.',
    relativeTime: { justNow: 'gerade eben', minutesAgo: 'vor {n} Min.', hoursAgo: 'vor {n} Std.', daysAgo: 'vor {n} Tagen' },
    unset: '(unbenannt)',
  },
  'fr-FR': {
    title: 'Gestion des sessions',
    refresh: 'Actualiser',
    loading: 'Chargement…',
    columns: {
      status: 'Statut', title: 'Titre', sessionId: 'ID de session', tenant: 'Locataire',
      turns: 'Tours', cost: 'Coût', tokens: 'Tokens', model: 'Modèle actuel',
      healthGrade: 'Note de santé', lastActive: 'Dernière activité', actions: 'Actions',
    },
    detail: {
      title: 'Détail de session', status: 'Statut', healthGrade: 'Note de santé', tenant: 'Locataire',
      apiKeyId: 'ID clé API', totalTurns: 'Total des tours', promptTokens: 'Tokens prompt',
      completionTokens: 'Tokens completion', totalCost: 'Coût total', currentCredential: 'Identifiant actuel',
      currentModel: 'Modèle actuel', titleLabel: 'Titre', annotation: 'Annotation', tags: 'Tags',
      rotationHistory: 'Historique de rotation ({count})', ongoing: 'en cours',
    },
    actions: { detail: 'Détail', stop: 'Arrêter', recover: 'Récupérer', close: 'Fermer' },
    states: {
      active: 'En cours', stopped: 'Arrêtée', recovered: 'Récupérée', waiting: 'En attente',
      error: 'Erreur', expired: 'Expirée', zombie: '⚠ Zombie',
    },
    healthGrades: { A: 'Excellent', B: 'Bon', C: 'Moyen', D: 'Faible', F: 'Critique' },
    confirm: { stopTitle: 'Arrêter la session', stopMessage: 'Arrêter cette session ?' },
    errors: {
      loadFailed: 'Échec du chargement des sessions : {msg}',
      stopFailed: 'Échec de l’arrêt : {msg}',
      recoverFailed: 'Échec de la récupération : {msg}',
      sseParseFailed: 'Échec d’analyse de l’événement SSE',
      serviceUnavailable: 'Service de session indisponible (HTTP 503). Réessayez plus tard.',
      httpStatus: 'Échec de la requête (HTTP {status})',
    },
    empty: 'Aucune session',
    emptyHint: 'Aucune session active dans la fenêtre actuelle. Les nouvelles sessions apparaissent après gw_session_id dans les logs.',
    relativeTime: { justNow: 'à l’instant', minutesAgo: 'il y a {n} min', hoursAgo: 'il y a {n} h', daysAgo: 'il y a {n} j' },
    unset: '(sans nom)',
  },
  'es-ES': {
    title: 'Gestión de sesiones',
    refresh: 'Actualizar',
    loading: 'Cargando…',
    columns: {
      status: 'Estado', title: 'Título', sessionId: 'ID de sesión', tenant: 'Inquilino',
      turns: 'Turnos', cost: 'Coste', tokens: 'Tokens', model: 'Modelo actual',
      healthGrade: 'Nota de salud', lastActive: 'Última actividad', actions: 'Acciones',
    },
    detail: {
      title: 'Detalle de sesión', status: 'Estado', healthGrade: 'Nota de salud', tenant: 'Inquilino',
      apiKeyId: 'ID de clave API', totalTurns: 'Total de turnos', promptTokens: 'Tokens prompt',
      completionTokens: 'Tokens completion', totalCost: 'Coste total', currentCredential: 'Credencial actual',
      currentModel: 'Modelo actual', titleLabel: 'Título', annotation: 'Anotación', tags: 'Etiquetas',
      rotationHistory: 'Historial de rotación ({count})', ongoing: 'en curso',
    },
    actions: { detail: 'Detalle', stop: 'Detener', recover: 'Recuperar', close: 'Cerrar' },
    states: {
      active: 'En ejecución', stopped: 'Detenida', recovered: 'Recuperada', waiting: 'En espera',
      error: 'Error', expired: 'Expirada', zombie: '⚠ Zombie',
    },
    healthGrades: { A: 'Excelente', B: 'Bueno', C: 'Medio', D: 'Deficiente', F: 'Crítico' },
    confirm: { stopTitle: 'Detener sesión', stopMessage: '¿Detener esta sesión?' },
    errors: {
      loadFailed: 'Error al cargar sesiones: {msg}',
      stopFailed: 'Error al detener sesión: {msg}',
      recoverFailed: 'Error al recuperar sesión: {msg}',
      sseParseFailed: 'Error al analizar evento SSE',
      serviceUnavailable: 'Servicio de sesión no disponible (HTTP 503). Inténtelo más tarde.',
      httpStatus: 'Solicitud fallida (HTTP {status})',
    },
    empty: 'Sin sesiones',
    emptyHint: 'No hay sesiones activas en la ventana actual. Aparecerán tras gw_session_id en los logs.',
    relativeTime: { justNow: 'ahora mismo', minutesAgo: 'hace {n} min', hoursAgo: 'hace {n} h', daysAgo: 'hace {n} d' },
    unset: '(sin nombre)',
  },
  'ja-JP': {
    title: 'セッション管理',
    refresh: '更新',
    loading: '読み込み中…',
    columns: {
      status: '状態', title: 'タイトル', sessionId: 'セッション ID', tenant: 'テナント',
      turns: 'ターン数', cost: 'コスト', tokens: 'トークン', model: '現在のモデル',
      healthGrade: 'ヘルスグレード', lastActive: '最終アクティブ', actions: '操作',
    },
    detail: {
      title: 'セッション詳細', status: '状態', healthGrade: 'ヘルスグレード', tenant: 'テナント',
      apiKeyId: 'API キー ID', totalTurns: '総ターン数', promptTokens: 'プロンプトトークン',
      completionTokens: '完了トークン', totalCost: '総コスト', currentCredential: '現在の認証情報',
      currentModel: '現在のモデル', titleLabel: 'タイトル', annotation: '注釈', tags: 'タグ',
      rotationHistory: '認証情報ローテーション履歴（{count}）', ongoing: '進行中',
    },
    actions: { detail: '詳細', stop: '停止', recover: '復旧', close: '閉じる' },
    states: {
      active: '実行中', stopped: '停止', recovered: '復旧済み', waiting: '待機中',
      error: 'エラー', expired: '期限切れ', zombie: '⚠ ゾンビ',
    },
    healthGrades: { A: '優秀', B: '良好', C: '普通', D: '不良', F: '異常' },
    confirm: { stopTitle: 'セッションを停止', stopMessage: 'このセッションを停止しますか？' },
    errors: {
      loadFailed: 'セッション一覧の読み込みに失敗: {msg}',
      stopFailed: 'セッション停止に失敗: {msg}',
      recoverFailed: 'セッション復旧に失敗: {msg}',
      sseParseFailed: 'SSE イベントの解析に失敗',
      serviceUnavailable: 'セッションサービス利用不可 (HTTP 503)。後でもう一度お試しください。',
      httpStatus: 'リクエスト失敗 (HTTP {status})',
    },
    empty: 'セッションなし',
    emptyHint: '現在の時間窓にアクティブなセッションはありません。リクエストログに gw_session_id が生成されると自動表示されます。',
    relativeTime: { justNow: 'たった今', minutesAgo: '{n} 分前', hoursAgo: '{n} 時間前', daysAgo: '{n} 日前' },
    unset: '（未命名）',
  },
  'ar-SA': {
    title: 'إدارة الجلسات',
    refresh: 'تحديث',
    loading: 'جاري التحميل…',
    columns: {
      status: 'الحالة', title: 'العنوان', sessionId: 'معرّف الجلسة', tenant: 'المستأجر',
      turns: 'الجولات', cost: 'التكلفة', tokens: 'الرموز', model: 'النموذج الحالي',
      healthGrade: 'درجة الصحة', lastActive: 'آخر نشاط', actions: 'إجراءات',
    },
    detail: {
      title: 'تفاصيل الجلسة', status: 'الحالة', healthGrade: 'درجة الصحة', tenant: 'المستأجر',
      apiKeyId: 'معرّف مفتاح API', totalTurns: 'إجمالي الجولات', promptTokens: 'رموز المطالبة',
      completionTokens: 'رموز الإكمال', totalCost: 'التكلفة الإجمالية', currentCredential: 'بيانات الاعتماد الحالية',
      currentModel: 'النموذج الحالي', titleLabel: 'العنوان', annotation: 'تعليق', tags: 'وسوم',
      rotationHistory: 'سجل تدوير الاعتماد ({count})', ongoing: 'جارية',
    },
    actions: { detail: 'تفاصيل', stop: 'إيقاف', recover: 'استعادة', close: 'إغلاق' },
    states: {
      active: 'قيد التشغيل', stopped: 'متوقفة', recovered: 'مستعادة', waiting: 'في الانتظار',
      error: 'خطأ', expired: 'منتهية', zombie: '⚠ زومبي',
    },
    healthGrades: { A: 'ممتاز', B: 'جيد', C: 'متوسط', D: 'ضعيف', F: 'حرج' },
    confirm: { stopTitle: 'إيقاف الجلسة', stopMessage: 'هل تريد إيقاف هذه الجلسة؟' },
    errors: {
      loadFailed: 'فشل تحميل الجلسات: {msg}',
      stopFailed: 'فشل إيقاف الجلسة: {msg}',
      recoverFailed: 'فشل استعادة الجلسة: {msg}',
      sseParseFailed: 'فشل تحليل حدث SSE',
      serviceUnavailable: 'خدمة الجلسات غير متاحة (HTTP 503). حاول لاحقًا.',
      httpStatus: 'فشل الطلب (HTTP {status})',
    },
    empty: 'لا توجد جلسات',
    emptyHint: 'لا توجد جلسات نشطة في النافذة الحالية. تظهر الجلسات الجديدة بعد إنشاء gw_session_id في سجلات الطلبات.',
    relativeTime: { justNow: 'الآن', minutesAgo: 'منذ {n} د', hoursAgo: 'منذ {n} س', daysAgo: 'منذ {n} ي' },
    unset: '(بدون اسم)',
  },
  'zh-TW': {
    title: '會話管理',
    refresh: '重新整理',
    loading: '載入中…',
    columns: {
      status: '狀態', title: '標題', sessionId: 'Session ID', tenant: '租戶',
      turns: '輪次', cost: '費用', tokens: 'Tokens', model: '目前模型',
      healthGrade: '健康等級', lastActive: '最後活躍', actions: '操作',
    },
    detail: {
      title: '會話詳情', status: '狀態', healthGrade: '健康等級', tenant: '租戶',
      apiKeyId: 'API Key ID', totalTurns: '總輪次', promptTokens: 'Prompt Tokens',
      completionTokens: 'Completion Tokens', totalCost: '總費用', currentCredential: '目前憑證',
      currentModel: '目前模型', titleLabel: '標題', annotation: '標註', tags: '標籤',
      rotationHistory: '憑證輪換歷史（{count}）', ongoing: '進行中',
    },
    actions: { detail: '詳情', stop: '停止', recover: '恢復', close: '關閉' },
    states: {
      active: '執行中', stopped: '已停止', recovered: '已恢復', waiting: '等待中',
      error: '異常', expired: '已過期', zombie: '⚠ 殭屍',
    },
    healthGrades: { A: '優秀', B: '良好', C: '一般', D: '較差', F: '異常' },
    confirm: { stopTitle: '停止會話', stopMessage: '確定要停止此會話嗎？' },
    errors: {
      loadFailed: '載入會話列表失敗：{msg}',
      stopFailed: '停止會話失敗：{msg}',
      recoverFailed: '恢復會話失敗：{msg}',
      sseParseFailed: 'SSE 事件解析失敗',
      serviceUnavailable: '會話服務不可用 (HTTP 503)，請稍後再試或聯絡管理員。',
      httpStatus: '請求失敗 (HTTP {status})',
    },
    empty: '暫無會話',
    emptyHint: '目前時間窗內暫無活躍會話。新會話將在請求日誌產生 gw_session_id 後自動聚合顯示。',
    relativeTime: { justNow: '剛剛', minutesAgo: '{n} 分鐘前', hoursAgo: '{n} 小時前', daysAgo: '{n} 天前' },
    unset: '（未命名）',
  },
}

const turnsByLocale = {
  'de-DE': {
    title: 'Kontext pro Runde', summaryLabel: 'Zusammenfassung bis zu dieser Runde',
    summaryEmpty: 'Noch keine Zusammenfassung (Titel oben generieren)', currentTurn: 'Aktuelle Runde',
    historyTurns: 'Verlaufsrunden', expandHistory: 'Verlauf erweitern ({n} Runden)', collapseHistory: 'Verlauf einklappen',
    stageOriginal: 'Original', stageCompressed: 'Komprimiert', stageSecured: 'Nach Sicherheit',
    directionSend: 'Senden', directionReceive: 'Empfangen', sendHint: '(nur aktuelle Runde)', sendHintCompressed: '(Panorama)',
    tokens: '{n} tok', saved: '{pct} gespart', rangeLabel: 'Runden {s}-{e}', noSecurityChange: 'Keine Sicherheitsänderung',
    appliedTags: 'Transformationen', tagPiiStrip: 'PII entfernt', tagStripTools: 'Tools entfernt', tagSensitive: 'Sensibel erkannt',
    tagAudit: 'Audit-Score', strategyLabel: 'Strategie', summaryMarker: 'enthält Komprimierungszusammenfassung',
    empty: 'Keine Rundendaten für diese Sitzung', loadError: 'Runden laden fehlgeschlagen',
  },
  'fr-FR': {
    title: 'Contexte par tour', summaryLabel: 'Résumé jusqu’à ce tour',
    summaryEmpty: 'Pas encore de résumé (générez un titre ci-dessus)', currentTurn: 'Tour actuel',
    historyTurns: 'Tours historiques', expandHistory: 'Développer l’historique ({n} tours)', collapseHistory: 'Réduire l’historique',
    stageOriginal: 'Original', stageCompressed: 'Compressé', stageSecured: 'Après sécurité',
    directionSend: 'Envoi', directionReceive: 'Réception', sendHint: '(tour actuel uniquement)', sendHintCompressed: '(panorama)',
    tokens: '{n} tok', saved: '{pct} économisé', rangeLabel: 'tours {s}-{e}', noSecurityChange: 'Aucun changement de sécurité',
    appliedTags: 'Transformations', tagPiiStrip: 'PII supprimé', tagStripTools: 'Outils supprimés', tagSensitive: 'Sensible détecté',
    tagAudit: 'Score d’audit', strategyLabel: 'Stratégie', summaryMarker: 'contient un résumé de compaction',
    empty: 'Aucune donnée de tour pour cette session', loadError: 'Échec du chargement des tours',
  },
  'es-ES': {
    title: 'Contexto por turno', summaryLabel: 'Resumen hasta este turno',
    summaryEmpty: 'Sin resumen aún (genere un título arriba)', currentTurn: 'Turno actual',
    historyTurns: 'Turnos históricos', expandHistory: 'Expandir historial ({n} turnos)', collapseHistory: 'Contraer historial',
    stageOriginal: 'Original', stageCompressed: 'Comprimido', stageSecured: 'Tras seguridad',
    directionSend: 'Envío', directionReceive: 'Recepción', sendHint: '(solo turno actual)', sendHintCompressed: '(panorama)',
    tokens: '{n} tok', saved: 'ahorrado {pct}', rangeLabel: 'turnos {s}-{e}', noSecurityChange: 'Sin cambios de seguridad',
    appliedTags: 'Transformaciones', tagPiiStrip: 'PII eliminado', tagStripTools: 'Herramientas eliminadas', tagSensitive: 'Sensible detectado',
    tagAudit: 'Puntuación de auditoría', strategyLabel: 'Estrategia', summaryMarker: 'contiene resumen de compactación',
    empty: 'Sin datos de turno para esta sesión', loadError: 'Error al cargar turnos',
  },
  'ja-JP': {
    title: 'ターン別コンテキスト', summaryLabel: 'このターンまでの要約',
    summaryEmpty: '要約なし（上でタイトルを生成）', currentTurn: '現在のターン',
    historyTurns: '履歴ターン', expandHistory: '履歴を展開（{n} ターン）', collapseHistory: '履歴を折りたたむ',
    stageOriginal: '元のセッション', stageCompressed: '圧縮セッション', stageSecured: 'セキュリティ処理後',
    directionSend: '送', directionReceive: '受', sendHint: '（このターンのみ）', sendHintCompressed: '（全景）',
    tokens: '{n} tok', saved: '{pct} 節約', rangeLabel: '第 {s}-{e} ターン', noSecurityChange: 'このターンにセキュリティ変更なし',
    appliedTags: '変換', tagPiiStrip: 'PII マスキング', tagStripTools: 'ツール削除', tagSensitive: '機密検出',
    tagAudit: '監査スコア', strategyLabel: '戦略', summaryMarker: '圧縮要約を含む',
    empty: 'このセッションにターンデータはありません', loadError: 'ターンの読み込みに失敗',
  },
  'ar-SA': {
    title: 'سياق كل جولة', summaryLabel: 'ملخص حتى هذه الجولة',
    summaryEmpty: 'لا يوجد ملخص بعد (أنشئ عنوانًا أعلاه)', currentTurn: 'الجولة الحالية',
    historyTurns: 'جولات سابقة', expandHistory: 'توسيع السجل ({n} جولات)', collapseHistory: 'طي السجل',
    stageOriginal: 'أصلي', stageCompressed: 'مضغوط', stageSecured: 'بعد الأمان',
    directionSend: 'إرسال', directionReceive: 'استلام', sendHint: '(الجولة الحالية فقط)', sendHintCompressed: '(بانوراما)',
    tokens: '{n} tok', saved: 'وفّر {pct}', rangeLabel: 'الجولات {s}-{e}', noSecurityChange: 'لا تغييرات أمنية هذه الجولة',
    appliedTags: 'تحويلات', tagPiiStrip: 'إزالة PII', tagStripTools: 'إزالة الأدوات', tagSensitive: 'حساس مكتشف',
    tagAudit: 'درجة التدقيق', strategyLabel: 'استراتيجية', summaryMarker: 'يتضمن ملخص ضغط',
    empty: 'لا توجد بيانات جولات لهذه الجلسة', loadError: 'فشل تحميل الجولات',
  },
  'zh-TW': {
    title: '逐輪上下文', summaryLabel: '到本輪為止的摘要',
    summaryEmpty: '暫無摘要（可在上方產生標題）', currentTurn: '目前輪',
    historyTurns: '歷史輪次', expandHistory: '展開歷史（{n} 輪）', collapseHistory: '收起歷史',
    stageOriginal: '原始會話', stageCompressed: '壓縮會話', stageSecured: '安全處理後',
    directionSend: '發', directionReceive: '收', sendHint: '（僅本輪最新訊息）', sendHintCompressed: '（全景）',
    tokens: '{n} tok', saved: '節省 {pct}', rangeLabel: '涵蓋第 {s}-{e} 輪', noSecurityChange: '本輪無安全變換',
    appliedTags: '變換', tagPiiStrip: 'PII 脫敏', tagStripTools: '工具裁剪', tagSensitive: '敏感檢出',
    tagAudit: '審計分', strategyLabel: '策略', summaryMarker: '含壓縮摘要',
    empty: '該會話暫無輪次資料', loadError: '載入輪次失敗',
  },
}

const retryByLocale = {
  'de-DE': 'Erneut versuchen', 'fr-FR': 'Réessayer', 'es-ES': 'Reintentar',
  'ja-JP': '再試行', 'ar-SA': 'إعادة المحاولة', 'zh-TW': '重試',
}

const compressionTipByLocale = {
  'de-DE': 'Für die vollständige Konfiguration der Komprimierungsstrategie besuchen Sie bitte die Seite „Komprimierungsverwaltung“.',
  'fr-FR': 'Pour la configuration complète de stratégie de compression, veuillez visiter la page « Gestion de compression ».',
  'es-ES': 'Para la configuración completa de estrategia de compresión, visite la página « Gestión de compresión ».',
  'ja-JP': '完全な圧縮戦略設定については「圧縮管理」ページをご覧ください。',
  'ar-SA': 'للحصول على تكوين كامل لاستراتيجية الضغط، يرجى زيارة صفحة «إدارة الضغط».',
  'zh-TW': '完整的壓縮策略設定請前往「壓縮管理」頁面',
}

const MGMT_TURNS_RE =
  /  management: \{\n    title: "会话管理"[\s\S]*?\n  \},\n  turns: \{[\s\S]*?\n  \},/

for (const locale of TARGETS) {
  const filePath = path.join(localesDir, locale, 'sessions.ts')
  let content = fs.readFileSync(filePath, 'utf8')
  if (!content.includes('会话管理')) {
    console.log(`skip ${locale}/sessions.ts: management already translated`)
    continue
  }
  const replacement = `${formatSection('management', managementByLocale[locale])}\n${formatSection('turns', turnsByLocale[locale])}`
  content = content.replace(MGMT_TURNS_RE, replacement)
  content = content.replace(/retry: "重试"/, `retry: ${formatValue(retryByLocale[locale], 0)}`)
  content = content.replace(
    /compressionTip: '[^']*'压缩管理\\"页面",/,
    `compressionTip: ${formatValue(compressionTipByLocale[locale], 0)},`,
  )
  content = content.replace(
    /compressionTip: "[^"]*压缩管理[^"]*",/,
    `compressionTip: ${formatValue(compressionTipByLocale[locale], 0)},`,
  )
  fs.writeFileSync(filePath, content, 'utf8')
  console.log(`fixed ${locale}/sessions.ts`)
}

console.log('done')
