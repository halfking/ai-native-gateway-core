## Métricas entre informes y conciliación

Antes de comparar informes, iguale el intervalo de tiempo y la zona horaria, el tenant, el alcance del tráfico, la unidad de recuento y la actualidad de los datos. Los intervalos estándar incluyen el inicio y excluyen el final. Las notas de la parte superior de la página describen el informe actual. Algunos informes todavía usan ventanas o fuentes distintas.

| Métrica | Cálculo estándar |
| --- | --- |
| Solicitudes de clientes | Cuente una sola vez cada solicitud de cliente registrada en la pasarela. Las solicitudes completadas incluyen éxitos, fallos, limitación de tasa y cancelaciones definitivas. Las solicitudes en curso se contabilizan por separado. Los reintentos y los cambios de proveedor mantienen el recuento original de solicitudes. |
| Envíos a proveedores | Cuente cada llamada enviada a un proveedor, incluidos los reintentos, los intentos en paralelo y la automatización de la plataforma. Un rechazo de la pasarela antes del envío cuenta solo como solicitud de cliente. Las instantáneas diarias actuales resumen los registros terminales de la pasarela. La cobertura completa por intento está pendiente. |
| Sesiones activas y nuevas | Cuente como activas las sesiones distintas con solicitudes de clientes en el intervalo. Considere nueva una sesión si su primera solicitud cae dentro del intervalo. Las solicitudes sin sesión se mantienen en el total de solicitudes. |
| Tasa de éxito | Éxitos divididos por eventos completados, calculado por separado para solicitudes de clientes y llamadas a proveedores. Un denominador de cero significa que no hay datos. |
| Tokens de entrada, salida y totales | Sume por separado los tokens de entrada y salida normalizados. El total de tokens equivale a la entrada más la salida. Muestre por separado los detalles de caché y razonamiento para evitar contarlos dos veces. |
| Tokens en caché y tasas de aciertos de caché | Sume por separado las lecturas y escrituras de caché. La tasa de aciertos por token divide los tokens leídos de la caché entre todos los tokens de entrada, incluidos los de entrada en caché, cuando los datos de caché son comparables. La tasa de aciertos por solicitud divide los aciertos entre las solicitudes con datos de caché comparables. Indique el denominador y la cobertura. |
| Coste estimado del proveedor | Calcule cada llamada usando el uso facturable, las tarifas de coste del proveedor vigentes en ese momento y su moneda. Registre por separado las tarifas o los datos de uso que falten. |
| Importe comunicado por el proveedor | Registre el importe y la moneda originales cuando la respuesta los indique explícitamente. Use la factura del proveedor para conciliar el importe final a pagar. |
| Créditos e importe internos | Sume los cargos contabilizados, las reversiones y los ajustes. Conserve la unidad y las reglas de conversión aplicables. Muestre por separado los créditos y la moneda. |
| Automatización de la plataforma | Asigne las sondas y las comprobaciones automáticas a un cliente específico. Inclúyalas en el coste del proveedor y en el uso de la plataforma, y exclúyalas de los cargos al tenant. Las notas de la página muestran el alcance actual del tráfico. |

Cero representa un valor conocido. Señale los datos de uso o precios ausentes, los elementos sin asignar, los cargos no contabilizados y los datos retrasados. Compare importes con la misma moneda y precisión.
