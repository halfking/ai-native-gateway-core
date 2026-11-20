# Explicación de los datos de la página de precios de los modelos estándar

Esta página define el «precio de venta»: cuánto paga el usuario por cada uso de un modelo estándar. Va en consonancia con la página de «precio de coste»: allí se compra; aquí, se vende.

## Reglas de precios (tres)

- **Una fila por modelo estándar**: al usuario solo le importa cuánto le cuesta usar ese modelo, no qué proveedor hay detrás.
- **Base global × factor de descuento**: las dimensiones sin precios específicos (entrada/salida/caché) se calculan automáticamente multiplicando el precio base global por el descuento.
- **Prioridad del precio manual**: los campos marcados como «manual» quedan congelados y los ajustes globales de precios no les afectan; sirve para mantener el precio de modelos especiales.

## Facturación multimodal

El volumen de texto de los modelos de imagen/audio/vídeo se factura por separado según su **tarifa específica**, sin mezclarlo con el texto normal; las filas de modelos con precios multimodales se identifican con la etiqueta «Incluye precios multimodales».

## Consistencia con otras páginas

- Los cambios en esta página **se aplican de inmediato** a la facturación de las llamadas posteriores; las tendencias del día y la factura se reflejan tras la siguiente instantánea.
- El precio de «Planes y recargas» que ve el usuario procede de esta página; si no coinciden, prevalecerá esta página y deberá revisarse la tarea de sincronización.

## Cómo comprobar las discrepancias numéricas

1. Si tras ajustar los precios no cambia el lado del usuario: compruebe si se olvidó de actualizar la caché en el lado del usuario o si el campo del modelo está marcado como «manual».
2. Dos precios de venta para el mismo modelo: puede que uno aplique el descuento global y el otro tenga un precio manual; consulte la marca «manual».
3. Si el importe de la factura no coincide con el registro de llamadas: compruebe el orden entre el momento de calcular el precio y el momento de ajustarlo; las facturas que cruzan un ajuste de precios se dividen en tramos.
