#!/usr/bin/env node
/**
 * Fix corrupted flat clusters/replay/contextLayout keys in sessions.ts
 */
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const localesDir = path.join(__dirname, '../src/locales')

const CORRUPT_TAIL =
  /(\s+loadError: "[^"]+"\s*\})\s*\n\s*clusters\.runCluster:[\s\S]*$/m

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

function formatSection(name, obj) {
  const lines = Object.entries(obj).map(
    ([k, v]) => `    ${k}: ${formatValue(v, 4)},`,
  )
  return `  ${name}: {\n${lines.join('\n')}\n  },`
}

const tailByLocale = {
  'de-DE': {
    clusters: {
      title: 'Sitzungs-Cluster',
      runCluster: 'Clustering starten',
      refresh: 'Aktualisieren',
      emptyTitle: 'Keine Cluster-Daten',
      emptyHint:
        'Klicken Sie auf „Clustering starten“, um ähnliche Sitzungen zu gruppieren. Modus in Modul-Einstellungen anpassen (rule/vector/hybrid).',
      loading: 'Cluster werden geladen…',
      unnamed: 'Unbenannter Cluster',
      sessionsCount: '{n} Sitzungen',
      avgCost: 'Durchschn. Kosten',
      quality: 'Qualität',
      detailTitle: 'Cluster-Details',
      membersTitle: 'Mitglieds-Sitzungen',
      ariaLabel: 'Cluster {label}, {count} Sitzungen',
      fields: {
        clusterId: 'Cluster-ID',
        coarseKey: 'Grob-Schlüssel',
        memberCount: 'Mitglieder',
        avgCost: 'Durchschn. Kosten',
      },
      table: {
        sessionId: 'Sitzungs-ID',
        title: 'Titel',
        cost: 'Kosten',
        similarity: 'Ähnlichkeit',
      },
      success: 'Clustering abgeschlossen — {n} Gruppen erstellt',
      errors: {
        loadFailed: 'Cluster konnten nicht geladen werden',
        clusterFailed: 'Clustering fehlgeschlagen',
        detailFailed: 'Details konnten nicht geladen werden',
      },
    },
    replay: {
      title: 'Sitzungs-Replay-Debug',
      subtitle:
        'Produktionssitzung lokal herunterladen und SessionCompressor / SessionCache abspielen, um Kompression, Cache und Zusammenfassung zu prüfen.',
      load: 'Laden',
      sessionIdPlaceholder: 'gw_session_id…',
      simulateModel: 'Modell simulieren',
      keepOriginalModel: '(Originalmodell beibehalten)',
      contextWindow: 'Kontextfenster (Tokens)',
      regenerateSummary: 'Zusammenfassung neu generieren',
      regenerating: 'Wird generiert…',
      exportReport: 'Bericht exportieren',
      turns: '{n} Runden',
      summaryTitle: 'Zusammenfassung (Quelle: {source})',
      aggregateTitle: 'Aggregierte Metriken',
      perStepTitle: 'Replay pro Runde',
      totalTurns: 'Gesamtrunden',
      strategy: 'Kompressionsstrategie',
      lossiness: 'Informationsverlust',
      cacheHits: 'Cache-Treffer',
      maxIn: 'Max. in',
      maxOut: 'Max. out',
      maxRatio: 'Max. Kompressionsrate',
      avgRatio: 'Durchschn. Kompressionsrate',
    },
    contextLayout: {
      backToList: '← Sitzungsliste',
      tabTopic: 'Mit Thema',
      tabNoTopic: 'Ohne Thema',
      refreshTitle: 'Aktualisieren',
      statHours: 'Zeitfenster',
      statTopic: 'Mit Thema',
      statNoTopic: 'Ohne Thema',
      statWindow: 'Aggregationsfenster',
      subtitle: 'Memora L1 Sitzungsspeicher und Gesprächsfäden',
    },
  },
  'fr-FR': {
    clusters: {
      title: 'Regroupements de sessions',
      runCluster: 'Lancer le clustering',
      refresh: 'Actualiser',
      emptyTitle: 'Aucune donnée de cluster',
      emptyHint:
        'Cliquez sur « Lancer le clustering » pour regrouper les sessions similaires. Mode configurable dans les paramètres du module (rule/vector/hybrid).',
      loading: 'Chargement des clusters…',
      unnamed: 'Cluster sans nom',
      sessionsCount: '{n} sessions',
      avgCost: 'Coût moyen',
      quality: 'Qualité',
      detailTitle: 'Détail du cluster',
      membersTitle: 'Sessions membres',
      ariaLabel: 'Cluster {label}, {count} sessions',
      fields: {
        clusterId: 'ID du cluster',
        coarseKey: 'Clé grossière',
        memberCount: 'Membres',
        avgCost: 'Coût moyen',
      },
      table: {
        sessionId: 'ID de session',
        title: 'Titre',
        cost: 'Coût',
        similarity: 'Similarité',
      },
      success: 'Clustering terminé — {n} groupes créés',
      errors: {
        loadFailed: 'Échec du chargement des clusters',
        clusterFailed: 'Échec du clustering',
        detailFailed: 'Échec du chargement du détail',
      },
    },
    replay: {
      title: 'Debug de relecture de session',
      subtitle:
        'Télécharger une session de production localement et rejouer SessionCompressor / SessionCache pour vérifier compression, cache et résumé.',
      load: 'Charger',
      sessionIdPlaceholder: 'gw_session_id…',
      simulateModel: 'Simuler le modèle',
      keepOriginalModel: '(conserver le modèle d’origine)',
      contextWindow: 'Fenêtre de contexte (tokens)',
      regenerateSummary: 'Régénérer le résumé',
      regenerating: 'Génération…',
      exportReport: 'Exporter le rapport',
      turns: '{n} tours',
      summaryTitle: 'Résumé (source : {source})',
      aggregateTitle: 'Métriques agrégées',
      perStepTitle: 'Relecture par tour',
      totalTurns: 'Total des tours',
      strategy: 'Stratégie de compression',
      lossiness: 'Perte d’information',
      cacheHits: 'Hits cache',
      maxIn: 'Max in',
      maxOut: 'Max out',
      maxRatio: 'Ratio de compression max',
      avgRatio: 'Ratio de compression moyen',
    },
    contextLayout: {
      backToList: '← Liste des sessions',
      tabTopic: 'Avec sujet',
      tabNoTopic: 'Sans sujet',
      refreshTitle: 'Actualiser',
      statHours: 'Fenêtre temporelle',
      statTopic: 'Avec sujet',
      statNoTopic: 'Sans sujet',
      statWindow: 'Fenêtre d’agrégation',
      subtitle: 'Mémoire de session Memora L1 et fils de conversation',
    },
  },
  'es-ES': {
    clusters: {
      title: 'Agrupaciones de sesiones',
      runCluster: 'Ejecutar clustering',
      refresh: 'Actualizar',
      emptyTitle: 'Sin datos de cluster',
      emptyHint:
        'Haga clic en « Ejecutar clustering » para agrupar sesiones similares. Ajuste el modo en la configuración del módulo (rule/vector/hybrid).',
      loading: 'Cargando clusters…',
      unnamed: 'Cluster sin nombre',
      sessionsCount: '{n} sesiones',
      avgCost: 'Coste medio',
      quality: 'Calidad',
      detailTitle: 'Detalle del cluster',
      membersTitle: 'Sesiones miembro',
      ariaLabel: 'Cluster {label}, {count} sesiones',
      fields: {
        clusterId: 'ID de cluster',
        coarseKey: 'Clave gruesa',
        memberCount: 'Miembros',
        avgCost: 'Coste medio',
      },
      table: {
        sessionId: 'ID de sesión',
        title: 'Título',
        cost: 'Coste',
        similarity: 'Similitud',
      },
      success: 'Clustering completado — {n} grupos creados',
      errors: {
        loadFailed: 'Error al cargar clusters',
        clusterFailed: 'Error en clustering',
        detailFailed: 'Error al cargar detalle',
      },
    },
    replay: {
      title: 'Depuración de reproducción de sesión',
      subtitle:
        'Descargue una sesión de producción localmente y reproduzca SessionCompressor / SessionCache para verificar compresión, caché y resumen.',
      load: 'Cargar',
      sessionIdPlaceholder: 'gw_session_id…',
      simulateModel: 'Simular modelo',
      keepOriginalModel: '(mantener modelo original)',
      contextWindow: 'Ventana de contexto (tokens)',
      regenerateSummary: 'Regenerar resumen',
      regenerating: 'Generando…',
      exportReport: 'Exportar informe',
      turns: '{n} turnos',
      summaryTitle: 'Resumen (origen: {source})',
      aggregateTitle: 'Métricas agregadas',
      perStepTitle: 'Reproducción por turno',
      totalTurns: 'Total de turnos',
      strategy: 'Estrategia de compresión',
      lossiness: 'Pérdida de información',
      cacheHits: 'Aciertos de caché',
      maxIn: 'Máx in',
      maxOut: 'Máx out',
      maxRatio: 'Ratio de compresión máx',
      avgRatio: 'Ratio de compresión medio',
    },
    contextLayout: {
      backToList: '← Lista de sesiones',
      tabTopic: 'Con tema',
      tabNoTopic: 'Sin tema',
      refreshTitle: 'Actualizar',
      statHours: 'Ventana temporal',
      statTopic: 'Con tema',
      statNoTopic: 'Sin tema',
      statWindow: 'Ventana de agregación',
      subtitle: 'Memoria de sesión Memora L1 e hilos de conversación',
    },
  },
  'ar-SA': {
    clusters: {
      title: 'تجميعات الجلسات',
      runCluster: 'تشغيل التجميع',
      refresh: 'تحديث',
      emptyTitle: 'لا توجد بيانات تجميع',
      emptyHint:
        'انقر «تشغيل التجميع» لتجميع الجلسات المتشابهة. يمكن ضبط الوضع في إعدادات الوحدة (rule/vector/hybrid).',
      loading: 'جاري تحميل التجميعات…',
      unnamed: 'تجميع بدون اسم',
      sessionsCount: '{n} جلسات',
      avgCost: 'متوسط التكلفة',
      quality: 'الجودة',
      detailTitle: 'تفاصيل التجميع',
      membersTitle: 'جلسات الأعضاء',
      ariaLabel: 'تجميع {label}، {count} جلسات',
      fields: {
        clusterId: 'معرّف التجميع',
        coarseKey: 'المفتاح الخشن',
        memberCount: 'الأعضاء',
        avgCost: 'متوسط التكلفة',
      },
      table: {
        sessionId: 'معرّف الجلسة',
        title: 'العنوان',
        cost: 'التكلفة',
        similarity: 'التشابه',
      },
      success: 'اكتمل التجميع — تم إنشاء {n} مجموعات',
      errors: {
        loadFailed: 'فشل تحميل التجميعات',
        clusterFailed: 'فشل التجميع',
        detailFailed: 'فشل تحميل التفاصيل',
      },
    },
    replay: {
      title: 'تصحيح إعادة تشغيل الجلسة',
      subtitle:
        'تنزيل جلسة إنتاج محليًا وإعادة تشغيل SessionCompressor / SessionCache للتحقق من الضغط والتخزين المؤقت والملخص.',
      load: 'تحميل',
      sessionIdPlaceholder: 'gw_session_id…',
      simulateModel: 'محاكاة النموذج',
      keepOriginalModel: '(الاحتفاظ بالنموذج الأصلي)',
      contextWindow: 'نافذة السياق (رموز)',
      regenerateSummary: 'إعادة إنشاء الملخص',
      regenerating: 'جاري الإنشاء…',
      exportReport: 'تصدير التقرير',
      turns: '{n} جولات',
      summaryTitle: 'الملخص (المصدر: {source})',
      aggregateTitle: 'مقاييس مجمعة',
      perStepTitle: 'إعادة تشغيل لكل جولة',
      totalTurns: 'إجمالي الجولات',
      strategy: 'استراتيجية الضغط',
      lossiness: 'فقدان المعلومات',
      cacheHits: 'إصابات التخزين المؤقت',
      maxIn: 'أقصى in',
      maxOut: 'أقصى out',
      maxRatio: 'أقصى نسبة ضغط',
      avgRatio: 'متوسط نسبة الضغط',
    },
    contextLayout: {
      backToList: '← قائمة الجلسات',
      tabTopic: 'بموضوع',
      tabNoTopic: 'بدون موضوع',
      refreshTitle: 'تحديث',
      statHours: 'النافذة الزمنية',
      statTopic: 'بموضوع',
      statNoTopic: 'بدون موضوع',
      statWindow: 'نافذة التجميع',
      subtitle: 'ذاكرة جلسة Memora L1 وخيوط المحادثة',
    },
  },
  'ja-JP': {
    clusters: {
      title: 'セッションクラスタ',
      runCluster: 'クラスタリング実行',
      refresh: '更新',
      emptyTitle: 'クラスタデータなし',
      emptyHint:
        '「クラスタリング実行」をクリックして類似セッションをグループ化。モジュール設定でモードを調整（rule/vector/hybrid）。',
      loading: 'クラスタを読み込み中…',
      unnamed: '名前なしクラスタ',
      sessionsCount: '{n} セッション',
      avgCost: '平均コスト',
      quality: '品質',
      detailTitle: 'クラスタ詳細',
      membersTitle: 'メンバーセッション',
      ariaLabel: 'クラスタ {label}、{count} セッション',
      fields: {
        clusterId: 'クラスタ ID',
        coarseKey: '粗キー',
        memberCount: 'メンバー数',
        avgCost: '平均コスト',
      },
      table: {
        sessionId: 'セッション ID',
        title: 'タイトル',
        cost: 'コスト',
        similarity: '類似度',
      },
      success: 'クラスタリング完了 — {n} グループ作成',
      errors: {
        loadFailed: 'クラスタの読み込みに失敗',
        clusterFailed: 'クラスタリング失敗',
        detailFailed: '詳細の読み込みに失敗',
      },
    },
    replay: {
      title: 'セッションリプレイデバッグ',
      subtitle:
        '本番セッションをローカルにダウンロードし、SessionCompressor / SessionCache を再生して圧縮・キャッシュ・要約を検証。',
      load: '読み込み',
      sessionIdPlaceholder: 'gw_session_id…',
      simulateModel: 'モデルシミュレート',
      keepOriginalModel: '（元のモデルを維持）',
      contextWindow: 'コンテキストウィンドウ（トークン）',
      regenerateSummary: '要約を再生成',
      regenerating: '生成中…',
      exportReport: 'レポート出力',
      turns: '{n} ターン',
      summaryTitle: '要約（ソース: {source}）',
      aggregateTitle: '集計メトリクス',
      perStepTitle: 'ターン別リプレイ',
      totalTurns: '総ターン数',
      strategy: '圧縮戦略',
      lossiness: '情報損失',
      cacheHits: 'キャッシュヒット',
      maxIn: '最大 in',
      maxOut: '最大 out',
      maxRatio: '最大圧縮率',
      avgRatio: '平均圧縮率',
    },
    contextLayout: {
      backToList: '← セッション一覧',
      tabTopic: 'トピックあり',
      tabNoTopic: 'トピックなし',
      refreshTitle: '更新',
      statHours: '時間窓',
      statTopic: 'トピックあり',
      statNoTopic: 'トピックなし',
      statWindow: '集計窓',
      subtitle: 'Memora L1 セッションメモリと会話スレッド',
    },
  },
  'zh-TW': {
    clusters: {
      title: '會話分組（聚類）',
      runCluster: '手動聚類',
      refresh: '重新整理',
      emptyTitle: '暫無聚類資料',
      emptyHint:
        '點擊「手動聚類」觸發一次相似會話分組。聚類模式可在模組設定中調整（rule/vector/hybrid）。',
      loading: '正在載入聚類…',
      unnamed: '未命名聚類',
      sessionsCount: '{n} 會話',
      avgCost: '平均成本',
      quality: '品質',
      detailTitle: '聚類詳情',
      membersTitle: '成員會話',
      ariaLabel: '聚類 {label}，{count} 個會話',
      fields: {
        clusterId: '聚類 ID',
        coarseKey: '粗聚類鍵',
        memberCount: '成員數',
        avgCost: '平均成本',
      },
      table: {
        sessionId: '會話 ID',
        title: '標題',
        cost: '成本',
        similarity: '相似度',
      },
      success: '聚類完成，產生 {n} 個分組',
      errors: {
        loadFailed: '載入聚類失敗',
        clusterFailed: '聚類失敗',
        detailFailed: '載入詳情失敗',
      },
    },
    replay: {
      title: '會話回放除錯',
      subtitle:
        '將生產會話下載到本地，回放 SessionCompressor / SessionCache，驗證壓縮 / 快取 / 摘要模組的實際行為。',
      load: '載入',
      sessionIdPlaceholder: 'gw_session_id…',
      simulateModel: '模擬模型',
      keepOriginalModel: '（保留原 model）',
      contextWindow: 'Context Window (tokens)',
      regenerateSummary: '重新產生摘要',
      regenerating: '產生中…',
      exportReport: '匯出報告',
      turns: '{n} 輪',
      summaryTitle: '摘要 (source: {source})',
      aggregateTitle: '聚合指標',
      perStepTitle: '每輪回放',
      totalTurns: '總輪次',
      strategy: '壓縮策略',
      lossiness: 'Lossiness',
      cacheHits: '快取命中',
      maxIn: '最大 in',
      maxOut: '最大 out',
      maxRatio: '最大壓縮比',
      avgRatio: '平均壓縮比',
    },
    contextLayout: {
      backToList: '← 會話列表',
      tabTopic: '有主題',
      tabNoTopic: '無主題',
      refreshTitle: '重新整理',
      statHours: '時間窗',
      statTopic: '有主題',
      statNoTopic: '無主題',
      statWindow: '聚合窗',
      subtitle: 'Memora L1 會話記憶與對話線索',
    },
  },
}

for (const [locale, sections] of Object.entries(tailByLocale)) {
  const filePath = path.join(localesDir, locale, 'sessions.ts')
  let content = fs.readFileSync(filePath, 'utf8')
  if (!CORRUPT_TAIL.test(content)) {
    console.log(`skip ${locale}/sessions.ts: no corrupt tail`)
    continue
  }
  const replacement = [
    '$1,',
    '',
    formatSection('clusters', sections.clusters),
    formatSection('replay', sections.replay),
    formatSection('contextLayout', sections.contextLayout).replace(/,$/, ''),
    '}',
  ].join('\n')
  content = content.replace(CORRUPT_TAIL, replacement)
  fs.writeFileSync(filePath, content, 'utf8')
  console.log(`fixed ${locale}/sessions.ts`)
}
