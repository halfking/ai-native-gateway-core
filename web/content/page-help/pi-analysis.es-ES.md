# Explicación de los datos de la página de análisis inteligente de errores

Esta página utiliza AI para ayudarte a analizar «por qué fallan las solicitudes»: entrega los errores de la cola de errores uno a uno (o en lotes) al agente inteligente para que determine las causas y proponga correcciones.

## Dos pestañas

- **Cola de errores**: Lista de fallos pendientes de análisis (cada fila representa una solicitud fallida).
- **Resultados del análisis**: Lista de conclusiones generadas por AI (clasificación de la causa, evidencia y recomendaciones).

## Criterios de cálculo de las seis tarjetas superiores

- Total / Pendientes de análisis / Analizados / Fallidos: estadísticas de la **cola de errores** (dominio de la cola).
- Tasa de falsos positivos / Tiempo medio: estadísticas de los **resultados del análisis** (dominio de resultados).
- El ámbito estadístico de las tres tarjetas es distinto del de las otras tres; sumarlas no tiene sentido: la página las presenta agrupadas por dominio.

## Cómo se aplican las recomendaciones de corrección

- Las recomendaciones de AI (modificar reglas/ajustar niveles/cambiar el patrón de coincidencia) requieren **aprobación humana** para aplicarse; durante la aprobación se aplica un umbral de confianza, y las recomendaciones con baja confianza se bloquean y se envían a revisión.
- Cada aplicación guarda una «instantánea del valor anterior», lo que permite revertirla con un solo clic.

## Cómo comprobar las discrepancias cuando las cifras no coinciden

1. El número de elementos de la cola no coincide con el número de fallos de la página de registros de solicitudes: la cola solo incluye los tipos de error **incluidos en el ámbito de análisis**; las solicitudes interceptadas previamente (como los fallos de autenticación) no entran en la cola.
2. Figuran como «analizados», pero no aparecen en la página de resultados: los resultados del análisis tardan un breve tiempo en guardarse en la base de datos; actualiza la página y vuelve a comprobarlo.
3. El mismo error aparece repetidamente: los errores con el mismo patrón se analizan de forma conjunta; la cola muestra una muestra representativa.
