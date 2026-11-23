# Explicación de los datos de la página de resumen del análisis de conversaciones

Esta página responde a «¿cómo lo utiliza la gente?»: cuántas conversaciones hay, cuánto duran de media, en qué tareas se emplea el tiempo y cuál es su evolución.

## Indicadores principales

| Tarjeta | Significado | Método de cálculo |
| --- | --- | --- |
| Número total de conversaciones | Cantidad de diálogos durante el período | Recuento deduplicado: cada conversación cuenta una vez, sin importar cuántas rondas tenga |
| Número medio de Token | Cantidad media de texto consumido por conversación | Cantidad total de Token de todas las conversaciones ÷ Número total de conversaciones |
| Duración media de la conversación | Tiempo promedio de duración de cada conversación | Se calcula la media de la diferencia entre la hora de inicio y la hora de fin de cada conversación |
| Número medio de rondas | Número medio de preguntas y respuestas por conversación | Número total de rondas ÷ Número total de conversaciones |
| Coste interno (USD) | Coste en dólares que la plataforma paga al proveedor | Se acumulan los importes de cada transacción según la tarifa del proveedor |
| Coste externo (créditos) | Créditos facturados a los inquilinos | Se acumulan los importes de cada transacción según la tarifa de la plataforma |

**El coste interno y el coste externo no se suman**: los dólares y los créditos son dos unidades distintas, y sumarlos no tiene ningún sentido empresarial. El gráfico de tendencias de la parte superior de la página también muestra cada unidad por separado.

## Tendencias y clasificaciones

- El gráfico de tendencias agrupa los datos por día (los días cambian según el tiempo universal coordinado, UTC) y muestra la evolución diaria del número de conversaciones o del coste.
- «Variación respecto al período anterior» = variación porcentual entre la ventana actual y una ventana anterior de la misma duración.
- Las clasificaciones (personas/agentes/modelos) se ordenan según la métrica seleccionada. El orden por «Gasto» usa el campo de compatibilidad del backend `total_cost`, que suma los valores numéricos originales del coste interno en USD y del coste externo en créditos, sin conversión de divisas. La lista puede seguir mostrando el valor con formato de dólares; por tanto, no es un importe ni permite comparar divisas. Consulte por separado el coste interno (USD) y el externo (créditos).

## Frescura de los datos

Esta página utiliza una «instantánea agregada» que el sistema resume periódicamente, y no el detalle de cada transacción. La instantánea tiene un límite de frescura (aproximadamente 90 minutos); cuando se supera, la página muestra el aviso «Los datos agregados se están actualizando» y no devuelve cifras obsoletas: es un mecanismo de protección, no un error.

## Cómo comprobar las discrepancias entre cifras

1. Intervalo temporal: esta página y la página de registros de solicitudes tienen ventanas temporales distintas, por lo que las cifras necesariamente no coinciden; primero, alinea los intervalos temporales.
2. Retraso de la instantánea: puede que las solicitudes más recientes todavía no se hayan incorporado a la instantánea agregada; vuelve a comprobarlo después de la actualización.
3. Diferencias de criterio: esta página agrega por «conversación», mientras que la página de registros de solicitudes se desglosa por «invocación individual»; por eso, los números de registros no tienen por qué coincidir.
4. Regla de deduplicación: el número de conversaciones se deduplica; las distintas rondas de una misma conversación no se cuentan como conversaciones adicionales; el número de rondas sí se cuenta ronda a ronda.
