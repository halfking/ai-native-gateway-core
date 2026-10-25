// landing.ts — Landing page copy (guest homepage).
//
// 2026-10-25 rewrite (impeccable craft-floor / STYLE pass).
// Rebuilt from the zh-CN / en-US samples. The previous draft was still the
// English marketing text: "Core Open Source · Enterprise-Grade · Private
// Deployment", adjectives standing in for facts, em dashes doing the job of
// punctuation, and every description a tidy "X + Y + Z".
// Rules live in DESIGN.md §7 "Voice" at the repo root.
//
// Fact discipline: this version claims only what the product actually does
// (Apache 2.0 / 40 MB image / 200 concurrent with P99 under 500 ms / RLS
// row-level security / 43 audit rounds with zero L1 findings / k3s and Docker
// / three inbound protocols / 15-day trial). No new capability is implied.
export default {
  // Rendered only in the PublicPageShell footer (brand name + this). It is no
  // longer used as a small label above the heading (craft-floor bans kickers).
  kicker: 'Núcleo bajo licencia Apache 2.0',
  brandTitle: 'Gateway central para organizaciones con IA nativa',
  brandSubtitle: 'Qigui: Instalación en tus instalaciones',
  // The heading no longer repeats the product name; the brand block above
  // already says that. It says what the thing does instead.
  title:
    'Un gateway para todas las llamadas a modelos de tu empresa, donde verlas, medirlas y apagarlas',
  subtitle:
    'Los protocolos OpenAI, Anthropic y Responses llegan al mismo gateway. Enrutado por inquilino y modelo, facturación, auditoría y límites de uso en un solo sitio. Se ejecuta en tus máquinas, así que el tráfico no sale.',
  featuresTitle: 'Qué hace',
  featuresSubtitle: 'Entrada, enrutado, facturación y auditoría, todo en el mismo acceso',
  // The six capability facts on the right-hand rail. Every one is checkable.
  heroPoints: [
    'Núcleo bajo Apache 2.0',
    'Plano de datos en Go, imagen de 40 MB',
    '200 peticiones simultáneas, P99 por debajo de 500 ms',
    'Tres protocolos de entrada, un solo punto de acceso',
    'Aislamiento por fila con PostgreSQL RLS',
    'k3s o Docker, en tu propio hardware',
  ],
  features: {
    smartRouting: {
      title: 'Enrutado por inquilino y por tarea',
      description:
        'Elige la ruta a partir del inquilino, el modelo y el tipo de tarea. Cuando un proveedor falla, la sonda lo detecta y el tráfico pasa al siguiente; un conjunto de huellas de credenciales se encarga de la rotación.',
    },
    safety: {
      title: 'Inyección de prompts y datos sensibles',
      description:
        'LLM-as-judge detecta la inyección de prompts (en v1 solo observa, no bloquea) y los campos sensibles se ocultan según reglas.',
      badge: 'beta',
    },
    cache: {
      title: 'Caché y coste',
      description:
        'Estabilización de prefijos más caché semántico. Un acierto mayor en KV Cache significa menos tokens facturados.',
    },
    agent: {
      title: 'Entrada de agentes y MCP',
      description:
        'Registro de agentes, protocolo A2A, alojamiento de herramientas MCP y traducción de protocolos. De ser un proxy de LLM a orquestar agentes.',
      badge: 'Próximamente',
    },
    observability: {
      title: 'Cada petición deja rastro',
      description:
        'Registros de peticiones, auditoría de decisiones de enrutado, trazas OTel y exportación de eventos SIEM/CEF. Cuando algo falla se puede reconstruir qué pasó.',
    },
    billing: {
      title: 'Uso que cuadra con la factura',
      description:
        'Planes, créditos y un monedero de tres bolsas (suscripción, crédito, recarga). Los inquilinos consultan su propio uso en lugar de preguntar a un administrador.',
    },
    multiProtocol: {
      title: 'Un punto de acceso, tres protocolos',
      description:
        'OpenAI Chat, Anthropic Messages y Responses se normalizan a la entrada. El cliente no cambia y el gateway tampoco.',
    },
    multiTenant: {
      title: 'Los inquilinos no se mezclan',
      description:
        'Seguridad por fila de PostgreSQL RLS, 43 rondas de auditoría sin ningún hallazgo L1, política y cuota independientes por inquilino.',
    },
  },
  advantagesTitle: 'Por qué instalarlo por tu cuenta',
  advantagesSubtitle: 'Los datos se quedan en tu lado y la factura se sigue leyendo',
  advantages: {
    openSource: {
      title: 'Núcleo abierto',
      description:
        'Plano de datos, plano de control, autenticación y planificador de rutas están abiertos bajo Apache 2.0. Puedes modificarlos y puedes auditarlos.',
    },
    private: {
      title: 'Tu propio hardware',
      description:
        'Se instala en tus máquinas, en forma de k3s o Docker, sin depender de ningún servicio externo.',
    },
    antiBan: {
      title: 'Un bloqueo no es una caída',
      description:
        'Rotación de credenciales y sondeos activos: cuando una credencial queda bloqueada, la siguiente sigue con el trabajo.',
    },
    perf: {
      title: 'Un plano de datos que aguanta',
      description:
        'Go nativo, imagen de 40 MB, P99 por debajo de 500 ms con 200 peticiones simultáneas y reenvío SSE estable.',
    },
  },
  footer: 'Núcleo abierto bajo Apache 2.0, ejecutándose en tus máquinas.',
  ariaPoints: 'Capacidades',
  roadmap: {
    title: 'Hacia dónde va',
    subtitle: 'De un plano de datos LLM a un gateway para agentes empresariales',
    v31: {
      phase: 'v3.1',
      title: 'Centro de activos de API Hub y alojamiento de herramientas MCP',
      description:
        'Los endpoints LLM, los servicios MCP y los agentes se registran en un mismo sitio, para que los desarrolladores los encuentren y los reutilicen por su cuenta.',
    },
    v32: {
      phase: 'v3.2',
      title: 'Bloqueo de inyecciones, integración con SIEM y SpecBoost',
      description:
        'La detección de inyección de prompts pasa de observar a bloquear, se ocultan los datos sensibles y las descripciones de API se enriquecen solas para mejorar la precisión de Function Calling.',
    },
    v40: {
      phase: 'v4.0',
      title: 'Registro de agentes y gateway de protocolo A2A',
      description:
        'Los agentes se delegan tareas entre sí, y OpenClaw y los agentes propios entran por la misma puerta.',
    },
    v50: {
      phase: 'v5.0',
      title: 'Soluciones sectoriales, disponibles',
      description:
        'Plantillas de atención al cliente, recursos humanos, ventas y logística. Instalas una y funciona.',
    },
  },
  activateBanner: {
    title: 'Este gateway no está activado',
    desc:
      'Esta instancia no tiene ninguna licencia válida asociada. Abre el asistente de activación para activarlo en línea o seguir el proceso sin conexión.',
    action: 'Activar',
  },
  downloadCta: {
    title: 'De la descarga a funcionando, cinco minutos',
    subtitle:
      'Paquetes sin conexión para cada plataforma. Sin registro para descargar y 15 días de prueba tras la instalación.',
    download: 'Descargar',
    support: 'Apoyarnos',
    activate: '¿Ya lo instalaste? Actívalo',
    note: 'Las donaciones son voluntarias y no afectan a la descarga ni a las funciones.',
  },
  deployFlow: {
    title: 'Descargar, instalar, activar, iniciar sesión',
    subtitle:
      'El camino completo de una instalación privada, con la activación incluida',
    steps: {
      download: {
        title: 'Descargar el paquete sin conexión',
        description:
          'Elige el tar.gz o el instalador según la plataforma y la arquitectura. Sin registro, y el enlace de descarga caduca.',
        action: 'Ir a la página de descarga',
      },
      install: {
        title: 'Instalarlo en la máquina destino',
        description:
          'Descomprime, ejecuta el instalador en el servidor y despliega en k3s o Docker con la configuración base escrita.',
        hint: 'llm-gw-installer install --target /opt/kx-gateway',
      },
      activate: {
        title: 'Activar la licencia',
        description:
          'Conectado, introduce la clave de licencia; sin conexión, sigue la vía activation.req hacia activation.resp. Primero hay 15 días de prueba.',
        action: 'Abrir el asistente de activación',
        offlineAction: 'Portal de activación sin conexión',
      },
      login: {
        title: 'Iniciar sesión en el plano de control',
        description:
          'Tras activar, entra con una cuenta de administrador. Ahí están los inquilinos, el enrutado y la observabilidad.',
        action: 'Iniciar sesión',
      },
    },
    notes: [
      'La descarga y la donación están separadas. Omitir la donación no quita ninguna función.',
      'En una red sin salida, envía activation.req por el portal de activación sin conexión',
      'El asistente de activación está en /activate y usa los mismos comandos que la CLI del instalador',
    ],
  },
  // 2026-07-21: guest-nav aria + top download/activation links (App.vue L197-203).
  guestNavAria: 'Navegación del producto',
  navDownload: 'Descargar',
  navSetup: 'Instalar y activar',
  navActivate: 'Activar en línea',
  navLicense: 'Estado de la licencia',
  navAgreement: 'Acuerdo de usuario',
  navSupport: 'Soporte',
  // 2026-07-21: landing primary CTAs (LandingView.vue L109-119).
  ctaLogin: 'Iniciar sesión',
  ctaDownload: 'Descargar el paquete',
  ctaActivate: 'Activar la licencia',
  ctaAgreement: 'Ver el acuerdo',
}
