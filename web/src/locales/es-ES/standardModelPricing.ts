// Auto-translated draft (es-ES) · 2026-07-02 · please review
// standardModelPricing.ts — Precios de modelos estándar.
export default {
  page: {
    title: 'Gestión de precios',
    refresh: 'Actualizar',
    refreshLoading: 'Cargando…',
  },
  desc: 'Precios de venta del lado del inquilino (créditos / 1M tokens). La base global × descuento se aplica a las dimensiones <strong>no manuales</strong>; los campos manuales no se ven afectados por cambios globales.',
  settings: {
    title: 'Base global (créditos / 1M tokens)',
    inputToken: 'Tokens de entrada',
    outputToken: 'Tokens de salida',
    cacheReadToken: 'Tokens de lectura de caché',
    cacheWriteToken: 'Tokens de escritura de caché',
    discount: 'Descuento global',
    discountHint: 'Aplicado uniformemente a todas las dimensiones no manuales',
    centsPerCredit: 'Precio unitario del crédito (céntimos/crédito)',
    currencyDisplay: 'Moneda mostrada',
    save: 'Guardar ajustes globales',
    saving: 'Guardando…',
    effective: 'Efectivo ≈ {value} (incl. descuento)',
  },
  filter: {
    summary: 'Manuales {custom}: Globales {defaults} / {total}',
  },
  table: {
    colModel: 'Modelo estándar',
    colStatus: 'Estado',
    edit: 'Precio',
    reset: 'Restablecer',
    empty: 'Sin modelos',
    statusDefault: 'Global',
    statusFullCustom: 'Totalmente manual',
    statusPartialCustom: '{n}/4 manual',
    manualTagTitle: 'Precio manual',
    manualTag: 'M',

    colModality: 'Modality',

    modalityText: 'Text',

    modalityVision: 'Vision',

    modalityAudio: 'Audio',

    modalityVideo: 'Video',

    modalityMultimodal: 'Multimodal',

    modalityEmbedding: 'Embedding',

    modalityOther: 'Other',

    hasMultimodal: 'Has manual multimodal pricing',
  },
  editModal: {
    title: 'Precio manual: {name}',
    hint: 'Marque "Manual" e introduzca los créditos; las dimensiones no marcadas siguen la base global × descuento.',
    fieldSuffix: ': precio manual',
    resetBase: 'Restablecer a la base',
    globalApprox: 'Global ≈ {value}',
    save: 'Guardar',
    saving: 'Guardando…',
    fillGlobal: 'Rellenar con el global actual',
    cancel: 'Cancelar',
    resetConfirm: '¿Restablecer todas las dimensiones de {name} a la base global?',

    sectionText: 'Text-token dimensions',

    sectionMultimodal: 'Dimensiones de tokens multimodales (visión / audio / vídeo)',



  },
  field: {

    image: 'Vision',

    audio: 'Audio',

    video: 'Video',
  },

  // 扁平键（供 Vue 组件直接使用）
  defaultOnly: 'Solo base global',
  customOnly: 'Con precio manual',
  partialOnly: 'Parcialmente manual',
  otherVendor: 'Otros',
  loadFailed: 'No se pudo cargar los precios de los modelos estándar. Vuelva a intentarlo.',
  saved: 'Base global guardada (solo afecta a las dimensiones sin precio manual)',
  saveFailed: 'No se pudieron guardar los precios de los modelos estándar. Vuelva a intentarlo.',
  needOneManual: 'Marque al menos una dimensión de «precio manual»',
  resetFailed: 'No se pudieron restablecer los precios de los modelos estándar. Vuelva a intentarlo.',
  input: 'Entrada',
  output: 'Salida',
  cacheRead: 'Lectura de caché',
  cacheWrite: 'Escritura de caché',

  batch: {
    selected: '{n} seleccionados',
    paste: 'Pegar en los seleccionados',
    price: 'Precios por lotes',
    resetAll: 'Restablecer todo a global',
    resetAllConfirm: '¿Restablecer todo el precio manual de {n} modelos a la base global?',
    fillGlobalConfirm: '¿Escribir la base global actual × descuento en {n} modelos (conservando las marcas manuales)?',
    msgPasted: '{n} modelos actualizados',
    msgReset: '{n} modelos restablecidos',
    msgFilled: '{n} modelos escritos',
    msgFailed: 'No se pudo completar la operación por lotes. Vuelva a intentarlo.',

    fillGlobal: 'Fill current global',
  },
}
