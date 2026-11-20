# Explicación de los datos de la página de detalles de la solicitud

Esta página muestra **el historial completo de una solicitud**: todo el proceso, desde su entrada en la pasarela hasta la devolución de la respuesta, junto con todas las cifras.

## Tres bloques

- **Información básica**: hora, clave, modelo, estado y duración total.
- **Detalle del proceso**: duración y resultado de cada etapa (autenticación, selección de ruta, llamada al proveedor, reinserción de datos enmascarados y medición de consumo).
- **Contenido y uso**: resumen del contenido realmente enviado/recibido, volumen de texto de entrada/salida y coste de esta solicitud.

## Criterios clave

- **Duración total**＝desde la recepción de la solicitud hasta la devolución de la respuesta completa; incluye dos partes: el tiempo de procesamiento de la pasarela y el tiempo de generación del proveedor, que se muestran por separado en el detalle.
- **Coste**: se calcula con la tabla de precios vigente en el momento de la solicitud; los cambios posteriores de precios no alteran el coste de las solicitudes históricas.
- El resumen del contenido puede mostrarse truncado (para proteger el rendimiento); para consultar el contenido completo, seleccione «Ver texto completo».

## Cómo comprobar las discrepancias numéricas

1. El coste de esta solicitud no coincide con el precio de catálogo del modelo: compruebe primero la hora de la solicitud y la del cambio de precios; el precio queda congelado en el **momento de la solicitud**.
2. El volumen de texto de entrada supera al que haya calculado manualmente: el recuento del sistema incluye el formato completo del mensaje (instrucciones del sistema, turnos anteriores), no solo lo que usted escribió.
3. El estado es «Correcto», pero el usuario dice que no recibió la respuesta: desde la pasarela, una solicitud correcta significa que «el proveedor devolvió la respuesta completa»; si después se corta la red del cliente, es un problema en el tramo final de la cadena. Consulte la cronología de acciones de la solicitud para confirmar la fase de devolución de la respuesta.
4. Indicios de reintentos: una solicitud puede probar internamente varios candidatos; el estado final solo refleja el último intento; el detalle del proceso muestra la cadena completa de intentos.
