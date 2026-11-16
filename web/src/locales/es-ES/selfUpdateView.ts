// selfUpdateView.ts — Actualización automática y mejora de versión (SelfUpdateView).
//
// Regla de redacción (misma base que el código de la página): que la
// comprobación falle, que no haya fuente remota configurada o que nunca se
// haya comprobado significa "sin conclusión posible". La página muestra un
// estado desconocido con el motivo y nunca afirma que la versión actual sea
// la más reciente.
export default {
  pageTitle: 'Actualización automática y mejora de versión',
  pageSubtitle:
    'El nodo busca nuevas versiones por su cuenta, genera un aviso e instala la mejora cuando un administrador la confirma',

  statusCardTitle: 'Estado de la versión actual',
  statusCardHint:
    'Muestra la versión que ejecuta este nodo y el resultado de la última comprobación de versiones remotas',
  currentVersion: 'Versión actual',
  latestVersion: 'Última versión disponible',
  buildSequence: 'Número de compilación',
  lastCheckedAt: 'Última comprobación',
  checkInterval: 'Intervalo de comprobación',
  checkIntervalHours: 'Comprueba automáticamente cada {hours} horas',
  checkIntervalMinutes: 'Comprueba automáticamente cada {minutes} minutos',
  checkIntervalSeconds: 'Comprueba automáticamente cada {seconds} segundos',
  checkNow: 'Comprobar actualizaciones ahora',
  checking: 'Comprobando',

  stateNotConfiguredLabel: 'Sin fuente de versiones remotas configurada',
  stateNotConfiguredDetail:
    'Este nodo no tiene configurada ninguna fuente de versiones remotas, por lo que la comprobación automática y los avisos están desactivados. No se puede determinar si existe una versión más reciente.',
  stateUnknownLabel: 'Estado de la versión desconocido',
  stateUnknownDetail:
    'La comprobación de versiones no dio ningún resultado: {reason}. Hasta que una comprobación funcione, no se puede determinar si existe una versión más reciente.',
  stateNeverCheckedDetail:
    'Este nodo tiene configurada una fuente de versiones remotas, pero todavía no se ha ejecutado ninguna comprobación. No se puede determinar si existe una versión más reciente.',
  stateUpToDateLabel: 'Ya está en la última versión',
  stateUpToDateDetail:
    'La comprobación remota confirmó que no hay ninguna versión más reciente disponible.',
  stateAvailableLabel: 'Hay una versión más reciente disponible',
  stateAvailableDetail:
    'La comprobación remota confirmó que la versión {version} está disponible y se puede instalar.',

  bannerTitle: 'Hay una versión más reciente disponible',
  bannerMandatory: 'Actualización obligatoria',
  bannerOptional: 'Actualización opcional',
  bannerNotesTitle: 'Notas de la versión',
  bannerNoNotes: 'Esta versión no incluye notas de versión.',
  installNow: 'Actualizar ahora',

  confirmInstallTitle: 'Confirmar la actualización a {version}',
  confirmInstallBody:
    'La actualización descarga el binario nuevo, lo verifica, sustituye el binario actual y reinicia el servicio. Durante el reinicio las peticiones se interrumpen un momento. ¿Continuar?',
  confirmInstallButton: 'Confirmar la actualización',
  installStartFailed: 'No se pudo iniciar la tarea de instalación: {reason}',

  installCardTitle: 'Progreso de la instalación',
  installCardHint:
    'Muestra el estado y los pasos ejecutados de la última tarea de instalación',
  installCardEmpty:
    'Este nodo todavía no tiene registrada ninguna tarea de instalación.',
  jobVersion: 'Versión de destino',
  jobMode: 'Método de instalación',
  jobModeUnknown: 'Método de instalación desconocido',
  jobStatus: 'Estado de la tarea',
  jobStatusPending: 'Esperando para empezar',
  jobStatusDownloading: 'Descargando el artefacto',
  jobStatusReadyToRestart: 'Esperando el reinicio del servicio',
  jobStatusUpgrading: 'Sustituyendo el binario',
  jobStatusSuccess: 'Actualización correcta',
  jobStatusFailed: 'La actualización falló',
  jobStatusRolledBack: 'Se volvió a la versión anterior',
  jobStatusUnknown: 'Estado desconocido',
  jobStartedAt: 'Hora de inicio',
  jobStepsTitle: 'Pasos ejecutados',
  jobStepsEmpty: 'La tarea todavía no ha generado ningún paso.',
  jobErrorTitle: 'Causa del fallo',
  jobSuccessNotice:
    'La actualización ha terminado y la versión nueva está en servicio.',
  jobRestartScheduled:
    'La actualización ha terminado, el servicio se reiniciará en breve.',
  modeArtifact: 'Descargar el artefacto e instalarlo',
  modeSource: 'Compilar desde el código local e instalar',

  notificationsTitle: 'Avisos de actualización',
  notificationsHint:
    'Muestra los avisos creados al detectar una versión nueva',
  notificationsEmpty: 'No hay avisos de actualización pendientes de tratar.',
  notificationsLoadFailed: 'No se pudo leer la lista de avisos: {reason}',
  notificationVersion: 'Versión',
  notificationStatus: 'Estado del aviso',
  notificationStartedAt: 'Creado el',
  notificationCompletedAt: 'Tratado el',
  notificationRetryCount: 'Número de reintentos',
  notificationPending: 'Pendiente de tratar',
  notificationHandled: 'Tratado',

  sourceCardTitle: 'Compilar desde el código local y actualizar',
  sourceCardHint:
    'Compila el binario a partir del código de esta máquina y lo instala. La compilación no modifica el árbol de trabajo actual.',
  sourceRefLabel: 'Referencia del código',
  sourceRefPlaceholder:
    'Déjalo vacío para usar el árbol de trabajo actual',
  sourceBuildNow: 'Compilar desde el código y actualizar',
  confirmSourceBuildTitle:
    'Confirmar la compilación desde el código y la actualización',
  confirmSourceBuildBody:
    'La compilación crea un árbol de trabajo aparte, genera el binario, sustituye la versión actual y reinicia el servicio. Usa CPU y disco de esta máquina. ¿Continuar?',
  confirmSourceBuildButton: 'Confirmar compilación y actualización',
  sourceBuildNoVersion:
    'Todavía se desconoce la versión que hay que instalar. Ejecuta primero una comprobación de versiones.',
  sourceBuildFailed: 'No se pudo iniciar la tarea de compilación: {reason}',
}
