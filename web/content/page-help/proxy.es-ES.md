# Explicación de los datos de la página de gestión de proxies

Esta página gestiona la «salida de proxy» que utiliza la pasarela para acceder al servicio de origen: fuente de suscripción, conjunto de nodos y salida utilizada actualmente.

## Tres pestañas

- **Resumen del estado**: tarjetas de resumen (número de suscripciones, total de nodos, nodos conectables/saludables) + salida seleccionada actualmente + estado del cambio automático de salida.
- **Gestión de suscripciones**: fuentes de suscripción de proxies (enlaces para importar lotes de nodos), con la opción de configurar una «lista negra de regiones» para filtrar las salidas.
- **Gestión de nodos**: registro y mantenimiento manual de cada nodo.

## Criterios clave

- **Conectable** = nodos que pueden establecer una conexión actualmente; **saludable** = nodos que han resultado normales en comprobaciones consecutivas. Los nodos conectables que fallan repetidamente no se consideran saludables.
- **Cambio automático de salida**: cuando la salida actual alcanza el umbral de fallos consecutivos, se cambia automáticamente al siguiente nodo; el «número de fallos consecutivos» se incrementa en tiempo real y se reinicia tras un éxito.
- La «última comprobación activa» es la hora de la comprobación de conectividad iniciada por el sistema, no la hora de las solicitudes de negocio.

## Cómo comprobar las discrepancias entre los datos

1. El total de nodos ≠ el número de nodos conectables: es normal, ya que el total corresponde a los nodos registrados y el número de nodos conectables, a los conectados en este momento.
2. El servicio sigue fallando después de un cambio de salida: el cambio de salida tiene un ciclo de sondeo y se completa en unos segundos; si los fallos persisten, compruebe si el «número de fallos consecutivos» sigue aumentando.
3. El número de nodos no cambia después de actualizar una suscripción: puede que el contenido de la fuente de suscripción sea el mismo; compare las huellas digitales de los nodos antes y después de la actualización, no el número.
4. La lista negra de regiones no surte efecto: el código de región debe coincidir exactamente con el valor de región notificado por el nodo (distingue entre mayúsculas y minúsculas); una configuración incorrecta no genera errores, pero tampoco filtra.
