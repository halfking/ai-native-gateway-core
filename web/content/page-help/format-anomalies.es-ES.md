# Explicación de los datos de la página de anomalías de formato

Esta página hace seguimiento de dos tipos de «algo que no cuadra»: el **formato** de la respuesta del servicio de origen cambia de repente o la solicitud **falla**. El objetivo es detectar los cambios en el proveedor antes que los usuarios.

## Dos fuentes de datos (dos pestañas)

- **Anomalía de formato en la respuesta**: la estructura de la respuesta del servicio de origen no coincide con lo previsto (faltan campos, han cambiado los tipos, no se ha podido extraer el Token, etc.).
- **Error de solicitud**: la propia solicitud falla (se agota el tiempo de espera, se rechaza, los parámetros son incorrectos, etc.).

El número del badge de la parte superior de la página es **la suma** de los elementos «sin resolver» de las dos fuentes de datos y se calcula igual que el filtro «Sin resolver» de la página de lista.

## Cómo se calcula «Sin resolver / Resuelto»

- Las anomalías recién detectadas se consideran «sin resolver» por defecto; cuando se confirma manualmente que han sido tratadas, se etiquetan como «Resuelto».
- «Resuelto» no significa que la anomalía desaparezca automáticamente: es un estado confirmado manualmente que sirve para distinguir entre «revisado» y «no revisado».

## Cómo comprobarlo cuando los números no coinciden

1. Si el número del badge no coincide con el número de elementos de la lista: comprueba primero los filtros (si está activada la opción «Solo sin resolver» y cuál es el intervalo de tiempo).
2. Si el proveedor rediseña su servicio, pueden aparecer simultáneamente varias anomalías del mismo tipo: revisa la agrupación por «Tipo de anomalía»; normalmente, un lote de anomalías del mismo tipo tiene una sola causa.
3. Tanto el tráfico de sondeo como el tráfico real pueden generar registros: antes de determinar el impacto, distingue de qué tipo de tráfico procede la anomalía.
4. La recuperación automática de una anomalía (cuando el proveedor vuelve a su versión anterior) no la marca automáticamente como «Resuelto»: todavía se necesita la confirmación manual. Esto es deliberado, para evitar que se ignoren cambios silenciosos.
