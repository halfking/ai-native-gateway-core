# Reglas de cálculo del panel y de las estadísticas de sesión

El «Panel» y las «Estadísticas de sesión» resumen el mismo flujo de llamadas a modelos con **dos reglas de cálculo distintas**. Esta página explica cómo se obtiene cada número de cada pestaña, de dónde salen los datos y qué ventana temporal cubren, e incluye una tabla de contraste con el motivo de las diferencias. Sirve para alinear datos y comprobar desvíos por cuenta propia.

> Base de la medición: 2026-11-23 02:27 (Asia/Shanghai) en producción, contrastado con consultas de solo lectura sobre la base de datos de producción.

## Cómo se relacionan las dos pestañas

| | Panel | Estadísticas de sesión |
| --- | --- | --- |
| Entidad estadística | Una llamada a modelo cuenta como una fila (**nivel de solicitud**) | Una sesión cuenta como una fila con sus valores **acumulados** (**nivel de sesión**) |
| Fuente de verdad | Tabla de hechos a nivel de solicitud `usage_facts` (línea base en Redis + delta en vivo) | Tabla resumen de sesiones `session_summaries` (consulta en vivo) + vistas materializadas (capa en caché) |
| Ventana temporal | Días naturales en la zona horaria del sistema: medianoche de hoy-(N-1) hasta ahora | Panel = N días móviles (primera solicitud dentro de los últimos N días); fila de contraste = días naturales completos de Shanghái |
| Tráfico de sondeo | Excluido según la clase de tráfico de la tabla de hechos | Excluido según las reglas de «universo de sesiones» del lado de sesiones |

**Conclusión primero**: los números de las dos pestañas **no deberían coincidir** — entidad distinta (solicitud frente a sesión), ventana distinta y reglas de exclusión distintas. Para comparar hay que alinear primero esos tres puntos; no vale con restarlos.

## Panel: cómo se obtiene cada número

| Tarjeta / módulo | Cálculo | Notas |
| --- | --- | --- |
| Solicitudes totales | Número de filas en estado final dentro de la ventana | Solo estados finales (correcto / fallido / limitado); no incluye flujos sin terminar |
| Tasa de éxito | Filas correctas ÷ filas totales | Porcentaje de solicitudes que devolvieron respuesta |
| Tokens totales | Suma de tokens de entrada y de salida de cada solicitud | Los tokens en caché siguen el criterio de cada proveedor |
| Coste total | Suma del coste de cada solicitud (dólares) | Los dólares son el coste pagado al proveedor |
| Créditos totales | Créditos que la plataforma imputa a los inquilinos | ★ véase «La tarjeta de créditos» |
| Latencia media | Suma de latencias ÷ filas totales | Milisegundos |
| Modelos / proveedores activos | Recuento distinto de los que realmente recibieron tráfico | No el «total disponible en la plataforma» |
| Claves activas | Número de claves actualmente habilitadas | **Según configuración, independiente de la ventana** |
| Recuento de errores | Suma por categoría de la distribución de fallos; si no existe, se estima con «solicitudes totales × (1 − tasa de éxito)» | La estimación puede diferir en unas pocas filas |
| Gráficos circulares (modelo / proveedor / inquilino / cliente…) | Solicitudes, tokens y coste agrupados por dimensión | Provienen de la tabla de estadísticas por minuto, no de la misma fuente que las tarjetas |
| Tendencia de uso | Solicitudes, tokens, coste y créditos por tramo de tiempo | También de la tabla de estadísticas por minuto |

### Cómo se calcula la ventana temporal

- «Hoy» = medianoche de hoy en la zona horaria del sistema (Asia/Shanghai por defecto) hasta ahora; «últimos 7 días» = medianoche de hace 6 días hasta ahora; «últimos 30 días» igual. **El inicio es la medianoche de un día natural y el final es el momento actual**, no el día completo.
- Un intervalo personalizado agrega los dos días elegidos, incluido el último día completo.
- La página se actualiza cada 10 segundos; con el canal en vivo activo, las solicitudes recién completadas se suman al instante.

### La tarjeta de créditos (regla especial, conviene saberlo)

La tarjeta de créditos **no** muestra los créditos consumidos dentro de la ventana elegida: la columna de créditos de la tabla de hechos a nivel de solicitud está siempre a 0 (medido: las 298.037 filas valen 0), de modo que lo que aparece es el **delta en vivo posterior a la línea base en caché**. Por tanto:

- Cambiar entre «Hoy / últimos 7 días / últimos 30 días» deja la tarjeta de créditos **exactamente igual** (medido: las tres muestran 86.925).
- La tendencia de uso de la misma página lleva otra suma de créditos (tabla por minuto: 1.767.183 en 7 días) que no coincide con la tarjeta.

Para un libro de créditos fiable use las páginas «Uso y coste» y «Conciliación»; la tarjeta de créditos del panel no sirve para comparar series temporales.

## Estadísticas de sesión: cómo se obtiene cada número

| Módulo | Cálculo | Notas |
| --- | --- | --- |
| Sesiones totales | Sesiones cuya primera solicitud cae en la ventana | Recuento a nivel de sesión; no es un número de solicitudes |
| Sesiones activas / nuevas / cerradas | Según si la última o la primera solicitud cae en las últimas 24 horas | Regla de 24 horas, independiente de la ventana principal |
| Coste total (USD) | Suma del coste acumulado de cada sesión | Todo el coste de una sesión queda en su fila |
| Solicitudes totales | Suma del número de solicitudes acumulado de cada sesión | También el valor acumulado de sesión, no las solicitudes ocurridas dentro de la ventana |
| Coste de entrada / salida | Suma del coste de entrada y de salida de cada sesión | Ambos suman el coste total |
| Coste medio por sesión / por solicitud | Coste total ÷ sesiones (÷ solicitudes) | El denominador es el valor acumulado de sesión |
| Tasa de éxito / de caché (fila de contraste) | Filas correctas ÷ solicitudes totales de la vista a nivel de solicitud | Solo existen a nivel de solicitud |
| Fila de contraste de análisis de sesión | Vista materializada agregada por sesión | Se actualiza cada 15 minutos; ventana = días naturales completos de Shanghái |
| Top 5 de usuarios / Top 5 de proveedores | API de resumen del análisis de sesión | El proveedor se deduce del prefijo del nombre de modelo y se ordena por solicitudes; **sin coste** |
| Contraste de turnos | Solicitudes de sesión frente a número de turnos | Si la diferencia supera el 10 %, la página muestra un aviso |

### El «universo de sesiones» (qué sesiones se cuentan)

Una sesión se cuenta solo si cumple las tres condiciones:

1. Está registrada en la tabla de sesiones;
2. Tiene propietario (clave de usuario o clave de propietario de API; si ambas están vacías no cuenta);
3. No es una sesión de sondeo: ni está marcada manualmente como sondeo ni coincide con el patrón «propietario / tipo de cliente / título» de autocomprobación, puerta de despliegue, prueba o verificación.

### Por qué aquí el coste es menor que en el panel

La tercera regla del universo de sesiones descarta **sesiones de negocio reales** clasificadas como sondeo. Medición en la base de datos de producción (misma ventana de 7 días, 2026-11-23):

- Las 15.898 sesiones excluidas por el lado de sesiones tienen su solicitud principal clasificada como **100 % tráfico de negocio** en la tabla de hechos, por un total de 6.452,73 $;
- en la misma ventana, el tráfico realmente clasificado como sondeo en la tabla de hechos suma solo **1,81 $**.

Es decir: ambas pestañas afirman excluir el sondeo, pero **no excluyen las mismas solicitudes**. Esta es la primera razón por la que el coste de las estadísticas de sesión queda claramente por debajo del del panel.

## Contraste de números (medido 2026-11-23, ventana de 7 días)

| Métrica | Panel | Estadísticas de sesión (panel) | Estadísticas de sesión (fila de contraste) |
| --- | --- | --- | --- |
| Entidad estadística | solicitudes | sesiones + solicitudes acumuladas | sesiones / solicitudes |
| Solicitudes | 65.774 | 72.098 | — |
| Coste (USD) | 8.013,93 | 2.717,62 | 2.468,73 (coste interno) |
| Créditos | 86.925 (independiente de la ventana) | — | 0 |
| Tokens | 3.245.804.009 | — | — |
| Tasa de éxito | 95,31 % | — | 94,64 % |
| Sesiones | — | 19.235 | 17.390 |

Correspondencia de cada diferencia:

- **Solicitudes 72.098 > 65.774**: el número de solicitudes del lado de sesiones es el acumulado de las sesiones elegidas, de modo que una sesión que cruza el límite de la ventana aporta íntegras sus solicitudes posteriores; además la tabla de sesiones contiene filas duplicadas (medido: hasta 30 filas con la misma clave de sesión) que también se agregan.
- **Coste 2.717,62 < 8.013,93**: el universo de sesiones elimina los 6.452,73 $ de tráfico de negocio descritos arriba; además el panel también cuenta llamadas **sin sesión asociada** (llamadas directas a la API, llamadas desde scripts), que miden alrededor de la quinta parte del coste del panel.
- **Créditos 0 frente a 86.925**: ni el lado de sesiones ni la tabla de hechos a nivel de solicitud tienen hoy existencias de crédito fiables; el valor mostrado procede del delta en vivo.
- **Tasa de éxito 95,31 % frente a 94,64 %**: ambas a nivel de solicitud, pero una sale de `usage_facts` y la otra de la vista materializada del registro de solicitudes, y con ventanas distintas.

## Qué revisar cuando los números no cuadran

1. **Alinee primero la ventana**: panel = desde la medianoche de un día natural; panel de estadísticas de sesión = N días móviles; fila de contraste = días naturales completos de Shanghái. Con el mismo preajuste siguen siendo tres ventanas distintas.
2. **Alinee después la entidad**: compare solicitudes con solicitudes y coste con coste; no compare número de sesiones con número de solicitudes.
3. **Busque marcas de degradación**: cuando una tarjeta muestra «degradado / no fiable / no calculado», esos ceros no significan «sin tráfico».
4. **Revise el conmutador de sondeo**: «excluir sondeo / incluir sondeo» en el panel solo cambia la cabecera; los gráficos de distribución y de tendencia siempre usan la base de negocio.
5. **«Hoy» puede fallar (corregido el 2026-11-23)**: el preajuste «Hoy» de las estadísticas de sesión agotaba de forma reproducible el tiempo de espera de 30 segundos porque faltaba el índice de la unión en la base de datos (índice añadido el 2026-11-23: la consulta pasó de 121,8 s a 0,19 s). Si la página vuelve a mostrar el error, el índice ha vuelto a desaparecer: pida al equipo de guardia que compruebe que `idx_session_dim_session_key` está en su sitio.

## Limitaciones conocidas (medido 2026-11-23)

1. **El coste y el número de solicitudes del lado de sesiones salen demasiado altos (recuento duplicado, pendiente de corregir)**: el coste y las solicitudes de las estadísticas de sesión proceden de la agregación de los resúmenes de sesión, pero `sessions` es un **historial de sesiones particionado por día** (la misma sesión recibe una fila nueva cada día) y la unión de la estadística no deduplica por sesión ⇒ la misma sesión se cuenta varias veces. Medición en la base de producción el 2026-11-23 (misma ventana de 7 días): el coste del lado de sesiones queda alrededor de un **61 %** y el número de solicitudes alrededor de un **36 %** por encima de los valores deduplicados por sesión (la sesión más cara se contó 3 veces). **Las cifras del panel (a nivel de solicitud) no se ven afectadas** ⇒ al comparar costes, use el panel o deduplique primero las cifras de sesión.
2. La agregación del lado de sesiones suma también las filas duplicadas de la tabla de sesiones, por lo que su número de solicitudes y su coste salen más altos de lo que deberían.
3. La tarjeta de créditos del panel no cambia con la ventana temporal (véase arriba).
4. La tendencia del panel (tabla por minuto) y las tarjetas (tabla de hechos a nivel de solicitud) difieren en torno al 13,8 % en la ventana de 30 días (160.451 frente a 140.969 solicitudes).
5. La exclusión de sondeo no retira el mismo tráfico en ambos lados (véase arriba).
