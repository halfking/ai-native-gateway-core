# Explicación de los datos de la página de clasificación de sesiones

Esta página divide el análisis de sesiones en tres clasificaciones: **quién lo utiliza** (personas), **quién está trabajando** (agentes), **qué se utiliza más** (modelos).

## Columnas de las tablas de clasificación

- Número de sesiones / Número de solicitudes / Número de Token: cada uno se cuenta de forma independiente (las sesiones se deduplican, cada solicitud se cuenta por separado y los Token se acumulan).
- Coste: **suma** del coste interno y los créditos externos; solo sirve para establecer comparaciones relativas y no debe interpretarse como un importe (dos unidades).
- Tasa de éxito / Tasa de acierto de caché / Duración media: métricas de eficiencia del elemento.

## Ordenación e intervalo temporal

- Seleccione el intervalo de tiempo en la parte superior de la página; las clasificaciones se ordenan por la métrica seleccionada y puede hacer clic en el encabezado de una columna para cambiar la clave de ordenación.
- «Cambio de posición» = variación de la posición respecto al intervalo temporal anterior de la misma duración; las entradas nuevas muestran «Nuevo».

## Cómo comprobar las discrepancias entre los números

1. La misma persona muestra un número de sesiones distinto en dos páginas: la página de clasificación lo agrega según el intervalo seleccionado; la página de detalle puede usar por defecto otro intervalo, así que primero debe alinear los intervalos temporales.
2. La columna «Coste» no coincide con la factura: es la suma de dos bases y solo sirve para comparar; para la conciliación, utilice necesariamente la página del informe de conciliación.
3. Los nombres anónimos/visibles no coinciden: se prioriza el nombre visible configurado y, si no existe, se muestra el nombre de la cuenta del sistema; tras cambiar el nombre, los datos históricos se agregan bajo la misma identidad y no se dividen en dos personas.
4. El total de la clasificación ≠ el total de la página de resumen: la clasificación solo muestra la lista Top; los elementos de poco volumen y de cola larga no aparecen, mientras que la página de resumen los incluye a todos.
