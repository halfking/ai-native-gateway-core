# Datos de la página de análisis avanzado de uso

Esta página presenta en paralelo los resultados de cinco API: resumen, tendencia de costes, comparación mensual, economía de caché y claves API principales. No son mediciones repetidas de una misma tabla ni de una misma ventana temporal.

## Tarjetas principales

- **Coste total (USD)**: importe total pagado al proveedor durante la ventana temporal.
- **Solicitudes totales**: número de solicitudes de la misma ventana temporal.
- **Variación respecto al periodo anterior**: porcentaje de comparación entre la ventana actual y la ventana anterior de igual duración.
- Desglose de detalles: porcentaje de contribución por dimensiones como modelo, clave o día.

## Criterios de cálculo

- La página combina cinco endpoints independientes. El resumen y las claves API principales usan una ventana por número de días; la tendencia de costes y la economía de caché usan el intervalo de fechas seleccionado; la tarjeta comparativa compara el mes natural actual con el anterior. Las fuentes y ventanas son distintas, así que no tienen por qué coincidir con el panel ni entre sí.
- El endpoint comparativo suma el coste en USD pagado a proveedores por mes natural y calcula (coste del mes actual − coste del mes anterior) ÷ coste del mes anterior. No comprueba que los meses estén completos. Si el coste del mes anterior es cero, la variación es cero; esto no demuestra que ambos meses estén completos ni que el uso no haya cambiado.

## Qué comprobar cuando las cifras no coinciden

1. Al comparar la tarjeta de periodo con otras métricas del intervalo seleccionado, recuerde que la tarjeta siempre compara el mes actual con el anterior y no sigue las fechas de inicio y fin seleccionadas.
2. La tendencia de costes agrupa por dimensión. Los valores de dimensión ausentes se agrupan como «unknown» y los grupos de cola larga pueden combinarse como «Other». Los totales pueden diferir si las fuentes o ventanas no coinciden plenamente.
3. El resumen y las claves API principales usan una ventana por número de días, mientras que la tendencia de costes y la economía de caché usan el intervalo seleccionado. Los límites de las ventanas, la actualización y el día actual aún abierto pueden causar diferencias. Si aparece un aviso de datos degradados, el cero o el resultado vacío no representa un cero medido.
