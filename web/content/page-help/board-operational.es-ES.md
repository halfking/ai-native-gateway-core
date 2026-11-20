# Explicación de los datos de la página del panel de operaciones

Esta página es la pantalla principal de trabajo del personal de operaciones: reúne en una sola vista los indicadores operativos dispersos (consumo, aprobaciones y tareas en segundo plano).

## Contenido

- **Tarjeta de resumen de consumo**: volumen de solicitudes, tasa de éxito, costes y otras cifras clave (misma fuente y mismos criterios que el panel).
- **Tareas en segundo plano**: estado y hora de la última ejecución de las tareas periódicas del sistema (agregación, detección, sincronización, etc.).
- **Resumen de comprobaciones**: vista rápida del estado de los componentes críticos.

## Cómo interpretar las tareas en segundo plano

- Cada tarea muestra «hora de la última ejecución + resultado»; «Fallido» no implica que el servicio no esté disponible: la mayoría de las tareas se reintentan automáticamente en el siguiente ciclo.
- Solo requiere atención una tarea que no se haya ejecutado durante un periodo prolongado (más de dos ciclos).

## Criterios de cálculo

- De forma predeterminada, las cifras de las tarjetas corresponden a «Hoy» (corte diario en UTC); al cambiar el intervalo de tiempo, coinciden con las del panel.
- Esta página no muestra totales que combinen distintas unidades; las tarjetas de gasto solo muestran un único criterio de cálculo explícito.

## Cómo comprobar las discrepancias entre cifras

1. Si las cifras de las tarjetas difieren en unas pocas unidades respecto al panel: es normal que el tráfico en tiempo real siga aumentando entre dos actualizaciones; cierre la ventana de «Hoy» o espere a la siguiente actualización.
2. Si todas las tareas en segundo plano aparecen como fallidas: compruebe primero si la capa de almacenamiento (la base de datos) no está disponible, ya que la mayoría de las tareas dependen de ella.
3. Si una tarea recién desplegada todavía no se ha ejecutado: se mostrará como «Sin ejecutar»; espere un ciclo.
