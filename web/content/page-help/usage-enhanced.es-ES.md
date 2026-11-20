# Datos de la página de análisis avanzado de uso

Esta página combina en una vista consolidada los datos de uso distribuidos entre cinco interfaces de API: coste total, solicitudes totales, variación respecto al periodo anterior y desglose de detalles, todo en una sola pantalla.

## Tarjetas principales

- **Coste total (USD)**: importe total pagado al proveedor durante la ventana temporal.
- **Solicitudes totales**: número de solicitudes de la misma ventana temporal.
- **Variación respecto al periodo anterior**: porcentaje de comparación entre la ventana actual y la ventana anterior de igual duración.
- Desglose de detalles: porcentaje de contribución por dimensiones como modelo, clave o día.

## Criterios de cálculo

- Los datos de esta página proceden de una interfaz de agregación con la misma fuente que el panel, por lo que las cifras deberían coincidir; si no coinciden, compruebe primero que las ventanas temporales sean las mismas.
- La variación respecto al periodo anterior solo se muestra si ambas ventanas cuentan con datos completos; si falta un día, la página indica «Datos incompletos» y el porcentaje debe tomarse solo como referencia.

## Qué comprobar cuando las cifras no coinciden

1. El porcentaje de variación resulta contraintuitivo (el uso no ha cambiado, pero muestra una variación elevada): unos datos incompletos en la ventana anterior distorsionan la base de referencia. Compruebe primero el grado de completitud de esa ventana.
2. El total del desglose no coincide con la tarjeta principal: las solicitudes sin valores de dimensión solo se contabilizan en el total (la página indica el volumen sin clasificar).
3. La ventana temporal incluye hoy: las cifras de hoy siguen aumentando en tiempo real y la variación respecto al periodo anterior continúa cambiando. Consultarlas una vez cerrado el día ofrece resultados más precisos.
