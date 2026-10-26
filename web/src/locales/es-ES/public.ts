// public.ts — Public portal copy (download / donation / offline activation).
//
// 2026-10-25 rewrite (impeccable craft-floor / STYLE pass): this file was
// still entirely English. It is now written in Spanish, and the decorative
// "·" separator, the em dashes standing in for punctuation and the
// triple-adjective lists are gone.
export default {
  download: {
    activateLink: 'Asistente de activación',
  },
  support: {
    emailPlaceholder: "you{'@'}company.com",
  },
  layout: {
    download: 'Descargar',
    support: 'Apoyarnos',
  },
  agreement: {
    title: 'Acuerdo de usuario y avisos',
    intro: 'Te damos la bienvenida al gateway central para organizaciones con IA nativa. El núcleo se comparte como código abierto y la versión estándar se entrega TAL CUAL (AS IS) para el despliegue en tus instalaciones. Lee esto antes de continuar:',
    points: [
      'Derechos y obligaciones: tú te encargas de la instalación, la configuración, las copias de seguridad y el uso conforme dentro de tu entorno. Nosotros entregamos el software y la documentación. No garantizamos resultados de negocio, calidad de los modelos ni la disponibilidad de terceros.',
      'Recogida de datos: por defecto no se recogen contenidos de negocio. La telemetría opcional cubre la versión, el identificador de la instancia, el estado de la licencia y métricas agregadas. Nunca cubre los prompts, el texto de las conversaciones, las claves API ni datos que identifiquen a una persona directamente.',
      'Compartir el código abierto: una versión sin adaptaciones es una aportación de la comunidad sin SLA comercial. Para un encargo a medida, escríbenos por el canal de soporte.',
      'Exención de responsabilidad: el software se entrega TAL CUAL (AS IS). No respondemos por pérdidas indirectas, tiempo de inactividad, pérdida de datos ni por las consecuencias de las decisiones de la IA. Evalúa el riesgo y toma las medidas que correspondan.',
    ],
    disclaimer: 'Las condiciones completas están en el acuerdo íntegro. Marcar la casilla significa que has leído y comprendido el alcance anterior.',
    checkbox: 'He leído y acepto la',
    fullLink: 'Acuerdo de usuario y autorización de tratamiento de datos',
    accept: 'Aceptar y continuar',
  },
  opAgreement: {
    downloadTitle: 'Lee {title} antes de descargar',
    activateTitle: 'Lee {title} antes de activar',
  },
}
