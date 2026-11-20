# Explicación de los datos de la página del árbol de etapas de la solicitud

Esta página divide la «vida» de una solicitud en diez etapas, ordenadas en una línea temporal, para poder ver de inmediato en qué paso se bloquea cuando se produce un problema.

## Diez etapas (en orden de aparición)

Autenticación/limitación de tasa → Enmascaramiento → Aprobación → Detección de inyección → Enrutamiento → Compresión → Proveedor/grupo de tokens → Primer byte → Restauración de datos enmascarados → Medición/respuesta.

- La pista de envío (izquierda→derecha) muestra el proceso por el que sale la solicitud; la pista de recepción (derecha→izquierda), el proceso por el que vuelve la respuesta.
- No todas las solicitudes recorren todas las etapas: las que no activan la aprobación muestran «No aplicable» en la etapa «Aprobación».

## Siete estados de las etapas

No alcanzado / En curso / Completado / No aplicable / Fallido / Con advertencias / Sin medir.

- «Fallido» = esta etapa ha bloqueado la solicitud (por ejemplo, cuando se detecta una inyección o se deniega la aprobación).
- «Con advertencias» = la solicitud se ha permitido, pero se han registrado indicios sospechosos.
- «Sin medir» = esta etapa no tiene datos de tiempo; esto no significa que haya fallido.

## Tiempo de cada etapa

- La longitud de la barra horizontal de cada etapa representa cuánto ha tardado; el tiempo desde «Primer byte» hasta la respuesta corresponde a la generación de la respuesta por el modelo y normalmente es el más largo.
- Tiempo total ≠ suma simple de las etapas: algunas etapas se ejecutan en paralelo.

## Qué comprobar cuando los números no cuadran

1. La duración de una etapa «En curso» sigue aumentando; el valor solo se fija cuando la etapa termina.
2. Si no encuentra una solicitud, compruebe primero los filtros: la página solo muestra las solicitudes que coinciden con los filtros actuales y están dentro del intervalo de tiempo seleccionado.
3. La presencia de muchas etapas «Sin medir» suele indicar que la solicitud utilizó el formato antiguo del flujo (datos de las primeras versiones). Es una forma histórica de los datos, no una avería del sistema.
4. En las solicitudes fallidas, la «etapa fallida» es la referencia: para localizar un problema, revise primero la primera etapa con estado «Fallido»; normalmente, las etapas posteriores no se ejecutan.
