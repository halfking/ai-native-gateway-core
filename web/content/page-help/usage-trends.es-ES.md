# Explicación de los datos de la página de tendencias de uso

Esta página utiliza un gráfico principal para responder a «¿cómo cambia el uso con el tiempo?»: permite desglosar los datos por modelo/clave/tenant en varias dimensiones, observar aumentos y disminuciones e identificar anomalías.

## Criterios de los gráficos

- La granularidad temporal puede ser «por hora/por día»; para la agregación diaria, los cortes de medianoche se definen según la hora universal estándar (UTC).
- Una línea plana (en cero) puede tener dos significados que deben distinguirse: **cero real** (no hubo uso) y **no calculado** (la fuente de datos aún no está disponible). Cuando la fuente de datos no está disponible, la página lo indica explícitamente; en ese caso, la línea plana no representa cero.
- Al seleccionar varios modelos, las curvas se muestran **por separado**, no superpuestas; para calcular el total, sume los valores diarios.

## Las condiciones de filtro se combinan con «y»

Tras filtrar después de seleccionar tiempo + modelo + clave, los resultados cumplirán simultáneamente todas las condiciones.

## Cómo comprobar las discrepancias entre las cifras

1. Si hay una diferencia con las cifras del panel: la página de tendencias obtiene los datos de la vista agregada y las solicitudes de los últimos minutos todavía no se han incluido en la agregación; como referencia, use las cifras en tiempo real del panel.
2. Si falta un intervalo de la curva cerca de medianoche: la agregación diaria cruza el punto de medianoche de UTC, mientras que las horas diurnas de la zona horaria de Beijing corresponden a la tarde del día anterior en UTC; un punto de inicio diferente de la ventana se debe a un criterio de cálculo distinto.
3. Si, al desglosar por dimensiones, la suma no coincide con la curva total: algunas solicitudes carecen de valores de dimensión (por ejemplo, porque el servicio de origen no devolvió el nombre del modelo); estas solo se incluyen en el total y no en el desglose: la página indicará el volumen de la parte no clasificada.
