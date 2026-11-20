# Explicación de los datos de la página de protección contra inyección de Prompt

Esta página configura la «protección contra instrucciones no autorizadas»: identifica contenido de ataque de tipo «ignorar las reglas/actuar como el sistema» intercalado en la entrada del usuario.

## Tres pestañas

- **Política**: interruptor general y modo de funcionamiento.
- **Motor LLM**: configuración del motor que utiliza un modelo para realizar una segunda valoración.
- **Reglas de detección**: lista de reglas específicas de patrones de ataque.

## Dos modos de funcionamiento (diferencia clave)

- **Modo de observación (solo registro)**: únicamente registra los resultados de detección y **no bloquea** ninguna solicitud; sirve para comprobar primero la tasa de falsos positivos antes de aplicar controles más estrictos.
- **Modo de aplicación**: ejecuta acciones según la «matriz de severidad» (permitir/requiere aprobación manual/bloquear directamente); un tiempo de espera de aprobación de 0 significa esperar indefinidamente.

## Criterios de cálculo

- Las coincidencias con las reglas se contabilizan como «detectadas»; en el modo de observación, **no producen** bloqueos y se mantienen separadas de las del modo de aplicación en las estadísticas.
- La matriz de severidad determina «cómo se gestiona la misma coincidencia en cada modo»: al cambiarla, revise las dos columnas a la vez.

## Cómo comprobar las discrepancias entre las cifras

1. Si se detecta una coincidencia pero la solicitud no se bloquea, lo más probable es que esté en modo de observación (es el comportamiento previsto); antes de cambiar al modo de aplicación, revise de nuevo los falsos positivos de esa regla.
2. Si la tasa de falsos positivos es alta: revise los ejemplos de coincidencias regla por regla; un falso positivo habitual es que una actividad empresarial normal incluya expresiones como «ignora las instrucciones anteriores»; ajuste esa regla en lugar de desactivar el interruptor general.
3. Si en el modo de aplicación todo se envía a aprobación: la matriz de severidad tiene configurados todos los niveles de severidad alta como «aprobación»; cuando el aprobador no está disponible, el resultado es que «todo queda en espera»; ajuste la matriz o el tiempo de espera de aprobación.
