# Explicación de los datos de la página de registro de solicitudes

Esta página es el registro detallado de todas las llamadas a modelos: cada solicitud se muestra en una fila y se puede revisar individualmente después de aplicar los filtros.

## Qué representa cada fila

Una fila = una llamada a un modelo. Incluye: hora de la llamada, clave y modelo utilizados, estado (éxito/fallo), tiempo transcurrido, volumen de texto de entrada/salida y coste de la solicitud.

## Reglas de la ventana temporal

- Se pueden elegir rangos rápidos (desde hace 1 hora hasta el mes actual) o personalizar las fechas y horas de inicio y fin.
- **El tenant no predeterminado solo puede consultar los últimos 3 días**: si se selecciona un intervalo más amplio, el sistema lo reduce automáticamente a 3 días. En la página, esto se verá como un retroceso del selector temporal.
- El tenant predeterminado tiene acceso a todos los intervalos, incluidos los últimos 7 días, esta semana, el mes actual y el año actual.

## Los filtros habituales se combinan con «Y»

Cuando se establecen varios criterios de filtrado (tiempo + clave + modelo + estado + proveedor), los resultados deben cumplir todas las condiciones a la vez. Cuantos más criterios se apliquen, menos resultados se obtendrán.

## Criterios de estado

- «Éxito» = el modelo ha devuelto correctamente una respuesta completa.
- Los estados de fallo se subdividen por motivo (error del proveedor, contenido bloqueado, tiempo de espera agotado, etc.); cada solicitud solo se clasifica en una categoría.

## Cómo comprobar las discrepancias cuando las cifras no coinciden

1. La causa más común es que las ventanas temporales no coincidan: se debe elegir el mismo intervalo en esta página y en la página de estadísticas.
2. Al aplicar filtros, se está viendo un «subconjunto»: para comprobar el total, hay que borrar todos los filtros.
3. El tenant asignado al usuario determina qué registros puede ver: un tenant no predeterminado solo puede ver las solicitudes de su propio tenant y únicamente las de los últimos 3 días.
4. Esta página muestra los detalles (y permite revisar cada solicitud), mientras que la página de estadísticas muestra un resumen (con eliminación de duplicados y agregación): para las comprobaciones puntuales, use esta página; para verificar el total, use la página de estadísticas.
