// landing.ts — Textos de la página de inicio (página de inicio de invitados).
//
// 2026-07-05: Actualizado con el contenido real del proyecto official-deploy actual (coincide con LandingView.vue).
export default {
  kicker: 'Código Abierto Principal · Localización China · Nivel Empresarial',
  title: 'LLM Gateway — Puerta de Enlace IA de Código Abierto para Mercados Globales',
  subtitle: 'La única puerta de enlace IA que combina código abierto principal con localización profunda en China. Gobernanza empresarial, acceso LLM global, cumplimiento y soberanía de datos — todo código abierto principal.',
  featuresTitle: 'Capacidades Principales',
  featuresSubtitle: 'Cubriendo aspectos clave desde el acceso hasta las operaciones',
  heroPoints: [
    'Código Abierto Principal · Apache 2.0',
    'Localización China · Djbh 2.0',
    'Puerta de Enlace IA Empresarial',
    'Gobernanza Vibe Coding',
    'Gestión de Activos de Sesión IA',
    'Escudo de Seguridad de Datos',
  ],
  features: {
    smartRouting: {
      title: 'Enrutamiento Inteligente y Pool de Credenciales',
      description: 'Enrutamiento automático por inquilino, modelo y tipo de tarea; pool de huellas digitales de múltiples credenciales + sondeo adaptativo, conmutación por error en segundos, tasa de prohibición casi nula.',
    },
    safety: {
      title: 'Escudo de Seguridad de Llamadas',
      description: 'Detección de inyección de prompt LLM-as-judge (modo de observabilidad v1) + planificación de enmascaramiento de datos sensibles, defensa de cumplimiento empresarial.',
      badge: 'beta',
    },
    cache: {
      title: 'Alineación de Caché y Reducción de Costos',
      description: 'Estabilización de prefijo de prompt + almacenamiento en caché semántico, maximización de la tasa de aciertos de KV Cache, reducción de sobrecarga de cómputo de tokens.',
    },
    agent: {
      title: 'Puerta de Enlace Agent y MCP',
      description: 'Registro de agentes, protocolo A2A, alojamiento de herramientas MCP y conversión de protocolo — actualización de proxy LLM a puerta de enlace de orquestación de agentes.',
      badge: 'Próximamente',
    },
    observability: {
      title: 'Observabilidad de Cadena Completa',
      description: 'Registros de solicitudes, auditoría de decisiones de enrutamiento, seguimiento OTel, exportación de eventos SIEM/CEF, listo para Djbh 2.0 y GDPR.',
    },
    billing: {
      title: 'Sistema de Facturación MaaS',
      description: 'Plan + créditos + billetera de tres niveles (suscripción / crédito / recarga), ciclo de comercialización completo para autoservicio de inquilinos.',
    },
    multiProtocol: {
      title: 'Compatibilidad Multi-Protocolo',
      description: 'OpenAI Chat / Anthropic Messages / Responses tres protocolos entrantes unificados, acceso fluido a modelos de código abierto y comerciales.',
    },
    multiTenant: {
      title: 'Aislamiento Multi-Inquilino',
      description: 'Seguridad a nivel de fila PostgreSQL RLS + 43 rondas de auditoría L1=0, cero filtración de datos entre inquilinos, política y cuota independientes por inquilino.',
    },
  },
  advantagesTitle: '¿Por Qué Elegir LLM Gateway?',
  advantagesSubtitle: 'Para empresas globales con necesidades comerciales en China',
  advantages: {
    local: {
      title: 'Localización Profunda en China',
      description: 'Interfaz completa en chino, acceso prioritario a LLM de código abierto doméstico, integración Alipay/WeChat Pay, plantillas de cumplimiento Djbh 2.0, infraestructura en la nube doméstica lista',
    },
    private: {
      title: 'Implementación Privada',
      description: 'Implementación totalmente privada, los datos permanecen en la empresa, forma dual k3s + Docker, cero dependencias externas',
    },
    antiBan: {
      title: 'Sistema Anti-Prohibición',
      description: 'Rotación de más de 50 UA + pool de huellas digitales TLS utls + 11 perfiles de navegador + rotación automática de 5 minutos',
    },
    perf: {
      title: 'Plano de Datos de Alto Rendimiento Go',
      description: 'Implementación Go nativa, imagen ligera de 40 MB, 200 concurrencias P99 < 500 ms, relevo estable de transmisión SSE',
    },
  },
  footer: 'LLM Gateway · llmgateway.internal.example.com · Código Abierto Principal · Localización China · Implementación Privada',
  ariaPoints: 'Aspectos Destacados Clave',
  roadmap: {
    title: 'Hoja de Ruta del Producto',
    subtitle: 'Desde el plano de datos LLM hasta la puerta de enlace Agent empresarial, construcción continua',
    v31: {
      phase: 'v3.1 · 2026 Q3',
      title: 'Centro de Activos API Hub + Alojamiento de Herramientas MCP',
      description: 'Registro unificado de puntos finales LLM, servicios MCP y Agents, descubrimiento y reutilización de autoservicio para desarrolladores.',
    },
    v32: {
      phase: 'v3.2 · 2026 Q4',
      title: 'Escudo de Seguridad GA + Integración SIEM + SpecBoost',
      description: 'Bloqueo de inyección de prompt, enmascaramiento de datos sensibles, enriquecimiento inteligente de descripción de API para mejorar la precisión de Function Calling.',
    },
    v40: {
      phase: 'v4.0 · 2027 Q1',
      title: 'Registro de Agentes + Puerta de Enlace de Protocolo A2A',
      description: 'Delegación de tareas entre agentes y orquestación, acceso unificado a OpenClaw y Agents de negocio.',
    },
    v50: {
      phase: 'v5.0 · 2027 Q3',
      title: 'Soluciones de Industria GA',
      description: 'Cuatro plantillas de industria para servicio al cliente, RRHH, ventas, logística, soluciones de agentes listas para usar.',
    },
  },
}
