# Descripción de los datos de la página de resumen

Esta página es el «panel de control» de toda la pasarela y reúne en una sola pantalla el uso, los costes y el estado de salud durante un periodo.

## De dónde proceden los datos

Todos los números de la página proceden del registro de cada llamada a un modelo efectuada a través de la pasarela. El sistema registra primero cada solicitud y después la agrega según el intervalo de tiempo seleccionado para obtener las distintas estadísticas.

## Cómo se calcula el intervalo de tiempo

- Los tres intervalos rápidos «Hoy», «Últimos 7 días» y «Últimos 30 días» se dividen por días naturales; el inicio de la ventana de estadísticas es la medianoche del día actual (hora universal Coordinada UTC).
- En un intervalo personalizado, se seleccionan dos días y se agrega todo el tráfico de ambos (desde la medianoche del primer día hasta la medianoche del último, incluido todo el último día).
- La página se actualiza automáticamente cada 10 segundos; si el canal en tiempo real está conectado, las solicitudes recién completadas se suman de inmediato, sin esperar la actualización.

## Cuatro tarjetas principales

| Tarjeta | Significado | Algoritmo |
| --- | --- | --- |
| Solicitudes totales | Número de llamadas a modelos procesadas durante este periodo | Recuento directo de las entradas del registro de tráfico |
| Tasa de éxito | Proporción de solicitudes que devolvieron una respuesta correctamente | Número de solicitudes con estado «Éxito» ÷ Número total |
| Total de Token | Volumen de texto consumido durante este periodo (entrada + salida) | Suma del volumen de Token de entrada y del volumen de Token de salida de cada solicitud |
| Coste total | Importe de la facturación en USD durante este periodo | Suma del coste de cada solicitud (en USD) |
| Créditos totales | Número de créditos según el criterio de facturación interno de la plataforma | Suma de los créditos deducidos en cada solicitud |

Nota: **El coste (en USD) y los créditos son dos criterios de facturación distintos y no se suman**. El coste corresponde al importe en USD que se paga al proveedor de origen; los créditos son la unidad con la que esta plataforma factura a los inquilinos. Sus valores no son comparables numéricamente y la página los muestra por separado.

## Número de errores y causas de los fallos

- El «número de errores» se calcula primero agregando por categorías los datos del gráfico circular de causas de fallo; si no hay gráfico, se estima mediante «Solicitudes totales × (1 − tasa de éxito)».
- Al pulsar la tarjeta del número de errores, se puede acceder a la distribución de errores y ver qué proporción representa cada tipo de fallo.

## Criterio de las etiquetas «Activo»

- «Modelos activos» = número de modelos que han tenido **tráfico real** durante este periodo, no el número total de «modelos disponibles en la plataforma».
- «Claves activas» = número de claves que se encuentran actualmente en estado **activado** (criterio de configuración, independiente del intervalo de tiempo).
- «Número de proveedores» = número de proveedores que han tenido **tráfico real** durante este periodo.

## Cómo comprobar las discrepancias cuando los números no coinciden

1. Primero, compruebe que el intervalo de tiempo sea el mismo: si las ventanas seleccionadas en dos páginas son diferentes, los números necesariamente serán distintos.
2. Compruebe si las tarjetas muestran la marca «Degradado»: cuando los datos agregados no se pueden calcular, la página lo indica explícitamente; en ese caso, el 0 no significa «que no hay tráfico».
3. Cuando la tarjeta de créditos muestra «No fiable/No calculado», significa que la fuente de datos de la contabilidad de créditos no está disponible temporalmente; el número de solicitudes y el coste siguen siendo reales.
4. El tráfico de sondeo no se incluye en las estadísticas de negocio: las solicitudes de comprobación de estado del propio sistema se excluyen de las solicitudes totales y del coste.
5. La actualización en tiempo real y el cálculo de los gráficos pueden diferir en unos segundos: los números de las tarjetas (incluidas las sumas en tiempo real) pueden ser unas pocas solicitudes superiores a los de los gráficos, lo cual es normal.
