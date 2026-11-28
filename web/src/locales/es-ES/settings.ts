// Auto-translated draft (es-ES) · 2026-07-02 · please review
// Updated to include settings.compression.* for Headroom modes (2026-07-02).
// settings.ts — Vista de ajustes.
export default {
  category: {
    all: 'Todos',
    compression: 'Compresión',
    rateLimit: 'Límite de tasa',
    timeout: 'Tiempo agotado',
    retry: 'Reintento',
    routing: 'Enrutamiento',
    session: 'Sesión',
    security: 'Seguridad',
    outputCompliance: 'Cumplimiento de salida',
    circuitBreaker: 'Disyuntor',
    loopGuard: 'Protección anti-bucle',
    general: 'Otros',
  },
 list: {
 count: '{n} ajustes',
 empty: 'No hay ajustes en esta categoría',
 colSetting: 'Ajuste',
 colCurrentValue: 'Valor actual',
 colSource: 'Fuente',
 colDanger: 'Riesgo',
 },
  dangerLevel: {
    note: '🟡 Aviso',
    warn: '🟠 Advertencia',
    danger: '🔴 Peligro',
  },
  detail: {
 tenantScopedTitle: 'Ajuste a nivel de tenant',
 tenantScopedBody: 'Este ajuste se aplica a un único tenant y no puede establecerse a nivel de sistema. Vaya a la página de Gestión de Tenants para configurarlo en un tenant concreto.',
 type: 'Tipo',
 currentValue: 'Valor actual',
 defaultValue: 'Valor predeterminado',
 options: 'Opciones',
 dangerLevel: 'Nivel de riesgo',
 hotReload: 'Recarga en caliente',
 noRequiresRestart: 'No (requiere reinicio)',
 observability: 'Punto de observación',
 detectField: 'Campo de detección {key}',
 emptyHint: '← Seleccione un ajuste a la izquierda para ver los detalles',
    errors: {
      loadListFailed: 'No se pudo cargar la lista de ajustes. Vuelva a intentarlo.',
      loadDetailFailed: 'No se pudo cargar el detalle de los ajustes. Vuelva a intentarlo.',
      saveFailed: 'No se pudo guardar el ajuste. Vuelva a intentarlo.',
      rollbackFailed: 'No se pudo revertir el ajuste. Vuelva a intentarlo.',
      invalidNumber: 'Introduzca un número válido',
    },
  },
  editor: {
 newValue: 'Nuevo valor',
 aliasPlaceholder: 'Escriba un alias y pulse Intro',
 aliasHint: 'Se guarda como cadena separada por comas y actualiza al instante la extracción de alias de sesión.',
 aliasPreviewTitle: 'Vista previa de solicitud de ejemplo',
 stringPlaceholder: 'Introduzca un valor de cadena',
 jsonPlaceholder: 'Introduzca un valor en formato JSON',
 jsonHint: 'Para tipos complejos use formato JSON',
 rollback: 'Revertir',
    rollbackConfirm: '¿Revertir {key} a su valor anterior?',

    enabledText: 'Activado',
    disabledText: 'Desactivado',
    saving: 'Guardando…',
    save: 'Guardar',
  },
  compression: {
    enumLabels: {
      off: '0 - Desactivado',
      auto: '1 - Umbral automático',
      on4xx: '2 - En 4xx',
      smart: 'Inteligente',
      aggressive: 'Agresivo',
    },
    enumDescriptions: {
      off: 'La compresión está completamente desactivada.',
      auto: 'Comprime automáticamente cuando la longitud del mensaje supera el umbral de la ventana de contexto.',
      on4xx: 'Comprime y reintenta al recibir un error 4xx (p. ej. context_length_exceeded).',
    },
    strategyEnum: {
      naive: 'naive - Compresión ingenua',
      smart: 'smart - Compresión inteligente',
      adaptive: 'adaptive - Compresión adaptativa',
    },
  },
 gateway: {
 modeEnum: {
 simple: 'simple - Modo simple',
 expert: 'expert - Modo experto',
 },
 recommended: '(Recomendado)',
 modeDescriptions: {
 simple: 'Modo simple: la página de inicio es una lista de accesos reducida; las herramientas expertas están plegadas en «Avanzado».',
 expert: 'Modo experto: navegación completa con todas las páginas (valor predeterminado actual).',
 },
 },
 timezone: {
    hint: 'La zona horaria determina cómo se agregan y muestran todos los rangos de tiempo (cortes de día, informes, consultas de registros). Predeterminado: UTC+8; se aplica inmediatamente después de guardar.',
    currentNow: 'Desfase horario actual: {offset}',
  },
  search: {
    placeholder: 'Buscar ajustes: clave / descripción / categoría…',
  },
  basicConfig: {
    toggle: 'Ajustes básicos',
    hotReload: 'Recarga en caliente',
    group: {
      identity: 'Identidad y apariencia',
      storage: 'Almacenamiento',
      schedules: 'Tareas programadas',
    },
    entry: {
      displayName: 'Nombre visible de la pasarela',
      skin: 'Tema predeterminado',
      timezone: 'Zona horaria del sistema',
      gatewayMode: 'Modo de funcionamiento',
      attachmentUrl: 'URL pública de adjuntos',
      attachmentDir: 'Directorio de almacenamiento de adjuntos',
      rollupHour: 'Hora de agregación (UTC)',
    },
    skin: {
      auto: 'Según el sistema',
      light: 'Claro',
      dark: 'Oscuro',
    },
    infraTitle: 'Infraestructura (solo lectura)',
    infraHint: 'Estado de ejecución procedente de la configuración de entorno en el arranque. Los cambios requieren reiniciar el servicio; las credenciales nunca se muestran en esta página.',
    infraNotConfigured: 'Sin configurar',
    infraNotSet: 'Sin establecer',
    infra: {
      instance: 'Identificador de instancia',
      redis: 'Redis (caché)',
      database: 'Base de datos',
      cacheDir: 'Directorio de caché (env)',
      attachmentDir: 'Directorio de adjuntos (env)',
    },
  },
  docs: {
    compressionModeTitle: '📖 Modo de compresión en detalle',
    compressionModeContent: `<p>El <strong>modo de compresión</strong> controla cómo el sistema maneja contextos de conversación demasiado largos:</p>
<ul>
  <li><code>0 (off)</code> - Desactiva la compresión; devuelve un error cuando el contexto excede.</li>
  <li><code>1 (auto_threshold)</code> - Modo predictivo; comprime proactivamente cuando los mensajes se acercan a la ventana de contexto.</li>
  <li><code>2 (on_4xx)</code> - Modo reactivo; comprime y reintenta tras un error 4xx. [Recomendado]</li>
</ul>
<p class="docs-note">💡 <strong>Se recomienda el modo 2</strong>: comprime solo cuando es necesario, evitando sobrecarga de rendimiento innecesaria.</p>`,
    cacheEnabledTitle: '📖 Caché de sesión en detalle',
    cacheEnabledContent: `<p>La <strong>caché de sesión</strong> controla si se activa la caché por niveles L1/L2/L3:</p>
<ul>
  <li><strong>L1</strong> - Caché en memoria (la más rápida)</li>
  <li><strong>L2</strong> - Caché en Redis (media)</li>
  <li><strong>L3</strong> - Caché en base de datos (la más lenta)</li>
</ul>
<p class="docs-note">⚠️ Cuando se desactiva, no se guarda estado de sesión; esto afecta a la continuidad del contexto.</p>`,
    formatConversionTitle: '📖 Conversión de formato en detalle',
    formatConversionContent: `<p>La <strong>conversión de formato</strong> permite la conversión automática del formato de solicitud entre protocolos:</p>
<ul>
  <li><strong>Ruta Q2</strong>: formato Anthropic → modelos OpenAI</li>
  <li><strong>Ruta Q3</strong>: formato OpenAI → modelos Anthropic</li>
</ul>
<p class="docs-note">💡 Se admiten anulaciones a nivel de proveedor; la conversión puede desactivarse para proveedores específicos.</p>`,
    rateLimitRpmTitle: '📖 Límite de tasa RPM en detalle',
    rateLimitRpmContent: `<p><strong>RPM (Requests Per Minute)</strong> limita el número de solicitudes por inquilino por minuto:</p>
<ul>
  <li>Adecuado para control de tráfico de grano grueso</li>
  <li>Usa un algoritmo de ventana deslizante, con precisión de segundos</li>
  <li>Devuelve HTTP 429 cuando se excede</li>
</ul>
<p class="docs-note">⚠️ <strong>A nivel de inquilino</strong>: este ajuste requiere un tenant_id; configúrelo en la página de Gestión de inquilinos.</p>`,
    rateLimitConcurrentTitle: '📖 Límite de tasa concurrente en detalle',
    rateLimitConcurrentContent: `<p>El <strong>límite concurrente</strong> limita el número de solicitudes concurrentes procesadas por inquilino:</p>
<ul>
  <li>Protege los recursos del sistema; evita que un único inquilino monopolice conexiones</li>
  <li>Basado en contador; respuesta rápida</li>
  <li>Devuelve 429 o pone en cola cuando se excede</li>
</ul>
<p class="docs-note">⚠️ <strong>A nivel de inquilino</strong>: este ajuste requiere un tenant_id; configúrelo en la página de Gestión de inquilinos.</p>`,
  },

  // 同步的扁平键
}
