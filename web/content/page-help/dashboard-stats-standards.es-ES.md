# Reglas de cálculo del panel y de las estadísticas de sesión

El «Panel», las «Estadísticas de sesión» y la «Conciliación de proveedores» resumen el mismo flujo de llamadas a modelos con **tres reglas de cálculo distintas**. Esta página explica cómo se obtiene cada número, de dónde salen los datos y qué ventana temporal cubren, e incluye un **desglose punto por punto** de por qué los números de los tres módulos no cuadran. Sirve para alinear datos y comprobar desvíos por cuenta propia.

> Base de la medición: 2026-11-23 21:05 (Asia/Shanghai) en producción. Todas las cifras se tomaron en el **mismo instante** (dentro de una única consulta SQL), ventana = `NOW()-7 días`; la conciliación de proveedores se midió aparte sobre el intervalo cerrado de días naturales `2026-11-17 ~ 2026-11-23` y se contrastó día a día con la tabla de hechos.
>
> ★ Corrección (ronda de alineación de rangos de fechas, 2026-11-23): la pestaña «estadísticas de sesión» usa ahora **el mismo** rango de fechas que la pestaña «panel» (mismo control, mismo intervalo de días naturales) ⇒ las cifras de estadísticas de sesión de abajo son medidas **previas a la alineación** (ventana móvil) y hay que volver a medirlas en la nueva ventana; las cifras del panel y de la conciliación no cambian.

## Cómo se relacionan los tres módulos

| | Panel | Estadísticas de sesión | Conciliación de proveedores |
| --- | --- | --- | --- |
| Entidad estadística | Una llamada a modelo cuenta como una fila (**nivel de solicitud**) | Una sesión cuenta como una fila con sus valores **acumulados** (**nivel de sesión**) | Una llamada a modelo cuenta como una fila (**nivel de solicitud**, agregada por proveedor) |
| Fuente de verdad | Tabla de hechos a nivel de solicitud `usage_facts` | Tabla resumen de sesiones `session_summaries` + universo de sesiones | Tabla de instantáneas diarias `report_snapshots` |
| Ventana temporal | Días naturales en la zona horaria del sistema: medianoche de hoy-(N-1) hasta ahora | **Mismo intervalo que el panel** (días naturales en la zona horaria del sistema: medianoche de hoy-(N-1) hasta el final del último día; inicio/fin personalizables) | **Intervalo cerrado de días naturales** (día completo, incluido el último día entero) |
| Tráfico de sondeo | Excluido según `traffic_class` (conmutable) | Excluido según las reglas del **universo de sesiones** | **No se excluye** (todo el tráfico, incluido «sin asignar») |
| Madurez | En vivo | En vivo | **Instantánea T+1** (las fechas cuyo día aún no ha terminado van con retraso) |

**Conclusión primero**: los números de los tres módulos **no deberían coincidir** — entidad distinta, ventana distinta, reglas de exclusión de sondeo distintas y madurez distinta. Para comparar hay que alinear primero esos cuatro puntos; no vale con restarlos.

### Lo más importante: el tráfico de sondeo es casi gratis

El tráfico de sondeo (health check / autocomprobación) figura por separado en la tabla de hechos como `traffic_class='probe'`:

| Base de cálculo (misma ventana) | Solicitudes | Coste |
| --- | --- | --- |
| Sin sondeo | 81.265 | 8.854,70 $ |
| Con sondeo | 189.534 | 8.856,87 $ |
| **Parte de sondeo** | **108.271** | **2,17 $** |

⇒ **El número de solicitudes se multiplica por 2,3, pero el coste solo cambia en 2,17 $.** Ante cualquier «muchas más solicitudes pero coste parecido», sospeche primero de la base de sondeo, no de un error de cálculo.

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
| Gráficos circulares (modelo / proveedor / inquilino / cliente…) | Solicitudes, tokens y coste agrupados por dimensión | Provienen del registro a nivel de solicitud, la misma fuente que las tarjetas (mismo alcance de sonda) |
| Tendencia de uso | Solicitudes, tokens, coste y créditos por tramo de tiempo | **Misma fuente que las tarjetas**: la misma tabla de hechos a nivel de solicitud `usage_facts` y el mismo predicado de sonda ⇒ los tramos suman la tarjeta |

### Cómo se calcula la ventana temporal

- «Hoy» = medianoche de hoy en la zona horaria del sistema (Asia/Shanghai por defecto) hasta ahora; «últimos 7 días» = medianoche de hace 6 días hasta ahora; «últimos 30 días» igual. **El inicio es la medianoche de un día natural y el final es el momento actual**, no el día completo.
- Un intervalo personalizado agrega los dos días elegidos, incluido el último día completo.
- La página se actualiza cada 10 segundos; con el canal en vivo activo, las solicitudes recién completadas se suman al instante.

### La tarjeta de créditos (regla especial, conviene saberlo)

La tarjeta de créditos **no** muestra los créditos consumidos dentro de la ventana elegida: la columna de créditos de la tabla de hechos a nivel de solicitud está siempre a 0 (medido: las 298.037 filas valen 0), de modo que lo que aparece es el **delta en vivo posterior a la línea base en caché**. Por tanto:

- Cambiar entre «Hoy / últimos 7 días / últimos 30 días» deja la tarjeta de créditos **exactamente igual** (medido: las tres muestran 86.925).
- La tendencia de uso y la tarjeta de créditos de la misma página provienen ahora de la **misma fuente** (ambas leen la tabla de hechos a nivel de solicitud `usage_facts`) ⇒ los créditos de los tramos suman la tarjeta.

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
| Top 5 de usuarios / Top 5 de proveedores | Endpoints de ranking del análisis de sesión (usuarios / modelos / proveedores / tareas) | El proveedor se agrega por SQL sobre `model_usage_aggregates.provider` (no se deduce del prefijo del nombre de modelo); **incluye coste** y admite ordenar por coste; las cuatro dimensiones Top 5 se muestran en Análisis de sesión → Usuarios |
| Contraste de turnos | Solicitudes de sesión frente a número de turnos | Si la diferencia supera el 10 %, la página muestra un aviso |

### El «universo de sesiones» (qué sesiones se cuentan)

Una sesión se cuenta solo si cumple las tres condiciones:

1. Está registrada en la tabla de sesiones;
2. Tiene propietario (clave de usuario o clave de propietario de API; si ambas están vacías no cuenta);
3. No es una sesión de sondeo: ni está marcada manualmente como sondeo ni coincide con el patrón «propietario / tipo de cliente / título» de autocomprobación, puerta de despliegue, prueba o verificación.

## Desglose completo de la diferencia de coste (medido 2026-11-23 en el mismo instante)

Los 81.265 / 8.854,70 $ del panel (sin sondeo), **desglosados punto por punto en cada bloque de las estadísticas de sesión** (las seis posiciones siguientes suman el total del panel, con el coste cerrando al céntimo):

| # | Componente | Solicitudes | Coste (USD) | Atribución |
| --- | --- | --- | --- | --- |
| C | Sesiones **conservadas** dentro del universo de sesiones | 58.180 | 1.551,41 | Estadísticas de sesión (esto es lo que muestra la página) |
| D1 | Excluidas · **sondeo marcado manualmente** | 12.364 | 5.145,16 | Brecha de las estadísticas de sesión |
| D2 | Excluidas · **sondeo detectado por patrón** | 5.946 | 1.876,67 | Brecha de las estadísticas de sesión |
| D3 | Excluidas · **sin propietario** | 1.202 | 0,08 | Brecha de las estadísticas de sesión |
| E | **Sesiones que cruzan la ventana** (primera solicitud fuera, activas dentro) | 3.191 | 281,38 | Brecha de las estadísticas de sesión |
| F | **Sin sesión asociada** (llamadas directas a la API, llamadas desde scripts) | 381 | 0,00 | Solo el panel |
| | **Total** | **81.263** | **8.854,70** | |

⇒ **La diferencia entre las estadísticas de sesión y el panel no está principalmente en las «sesiones que cruzan la ventana», sino en las reglas de exclusión de sondeo del universo de sesiones (D1+D2).**

### Medición clave: todo lo excluido es tráfico de negocio real

Las solicitudes dentro de la ventana de las sesiones excluidas D1/D2/D3, clasificadas por el `origin_stage` del registro de solicitudes:

| Motivo de exclusión | Solicitudes | Coste (USD) | De ellas `origin_stage='business'` | Porcentaje |
| --- | --- | --- | --- | --- |
| Sondeo marcado manualmente | 12.364 | 5.145,16 | 12.364 | **100 %** |
| Sondeo detectado por patrón | 5.946 | 1.876,67 | 5.946 | **100 %** |
| Sin propietario | 1.202 | 0,08 | 1.202 | **100 %** |

⇒ Las 19.512 solicitudes y 7.021,91 $ que el lado de sesiones excluye son, en el registro de solicitudes, **al 100 % tráfico de negocio (`origin_stage='business'`)**: ni una sola es de sondeo. **Esta es la primera razón por la que las estadísticas de sesión quedan por debajo del panel, y también la mayor desviación individual del momento.**

## Contraste de números de los tres módulos (medido 2026-11-23)

| Métrica | Panel (sin sondeo) | Estadísticas de sesión (panel) | Conciliación de proveedores |
| --- | --- | --- | --- |
| Entidad estadística | solicitudes | sesiones + solicitudes acumuladas de sesión | solicitudes (agregadas por proveedor) |
| Solicitudes | 81.265 | 58.180 (dentro del universo) / 77.687 (total fuera del universo) | 178.841 (incluido «sin asignar») |
| Coste (USD) | 8.854,70 | 1.551,41 (dentro del universo) | 8.612,07 |
| Tráfico de sondeo | ya excluido | excluido según las reglas de sesión | **no se excluye** |
| Madurez | en vivo | en vivo | **instantánea T+1** |

### Panel frente a conciliación de proveedores: contraste día a día

El lado de conciliación lee `daily_total` de `report_snapshots` y lo contrasta día a día con la tabla de hechos (intervalo cerrado de días naturales 11/17 ~ 11/23):

| Fecha | Solicitudes en la tabla de hechos | Solicitudes en la instantánea de conciliación | Diferencia | Diferencia de coste |
| --- | --- | --- | --- | --- |
| 11/17 | 27.446 | 27.446 | 0 | 0,0000 |
| 11/18 | 22.905 | 22.905 | 0 | 0,0000 |
| 11/19 | 35.008 | 35.008 | 0 | 0,0000 |
| 11/20 | 23.947 | 23.947 | 0 | 0,0000 |
| 11/21 | 21.341 | 21.341 | 0 | 0,0000 |
| 11/22 | 19.693 | 19.693 | 0 | 0,0000 |
| 11/23 | 36.019 | 28.501 | 7.518 | 42,38 |

⇒ **Los 6 primeros días coinciden solicitud por solicitud y céntimo por céntimo**; la diferencia del 11/23 proviene al 100 % del momento de corte de la instantánea de ese día (instantánea `updated_at` = 17:38, hora de medición 21:05): **no es un error de cálculo, es el retraso normal de un día que no ha terminado**.

### Dentro del módulo de conciliación: cero recuentos duplicados

Las filas de proveedor de la página de conciliación (incluido «sin asignar») **suman exactamente el total de la página**:

- Suma de las filas de proveedor = 178.841 solicitudes = el total de solicitudes de la página ✓
- Suma del coste de las filas de proveedor = 8.612,07 $ = el coste total de la página ✓
- De ellas, «sin asignar» (`provider_id IS NULL`) = 79.801 solicitudes / 100 % de fallos / 0,00 $ — es **tráfico de sondeo sin proveedor asignado** (el sondeo no llega al proveedor de aguas arriba), no un conteo que falte.

## Qué revisar cuando los números no cuadran

1. **Alinee primero la ventana**: panel y panel de estadísticas de sesión = **el mismo intervalo de días naturales** (medianoche de hoy-(N-1); el panel cuenta hasta ahora, el de estadísticas hasta el final del último día ⇒ ambos cubren los mismos datos y usan el mismo control de rango de fechas); fila de contraste = días naturales completos de Shanghái; conciliación de proveedores = intervalo cerrado de días naturales. Los dos últimos siguen sin ser la misma ventana que los dos primeros.
2. **Alinee después la entidad**: compare solicitudes con solicitudes y coste con coste; no compare número de sesiones con número de solicitudes.
3. **Alinee después la base de sondeo**: el panel permite conmutar a «incluir sondeo»; las estadísticas de sesión excluyen según las reglas del universo de sesiones; **la conciliación siempre incluye el sondeo**. Si el número de solicitudes difiere mucho y el coste es parecido ⇒ revise primero el sondeo.
4. **Alinee después la madurez**: la conciliación es una instantánea T+1, **los datos del día en curso van necesariamente con retraso**; antes de comparar, confirme hasta qué día llega la instantánea.
5. **Busque marcas de degradación**: cuando una tarjeta muestra «degradado / no fiable / no calculado», esos ceros no significan «sin tráfico».
6. **La tendencia y las tarjetas del panel tienen ahora la misma fuente**: ambas leen la tabla de hechos a nivel de solicitud `usage_facts` con el mismo predicado de sonda ⇒ los tramos deberían sumar la tarjeta. Si no: comprueba primero la marca `degraded` de la carga útil (el alcance de sonda difiere al recurrir a las tablas preagregadas) y después que las ventanas temporales coincidan.
7. **«Hoy» puede fallar (corregido el 2026-11-23)**: el preajuste «Hoy» de las estadísticas de sesión agotaba de forma reproducible el tiempo de espera de 30 segundos porque faltaba el índice de la unión en la base de datos (índice añadido el 2026-11-23: la consulta pasó de 121,8 s a 0,19 s). Si la página vuelve a mostrar el error, el índice ha vuelto a desaparecer: pida al equipo de guardia que compruebe que `idx_session_dim_session_key` está en su sitio.

## Limitaciones conocidas (medido 2026-11-23)

1. **El universo de sesiones excluye el sondeo demasiado a fondo (D1/D2, confirmado por medición el 2026-11-23)**: las 19.512 solicitudes y 7.021,91 $ que excluyen las estadísticas de sesión son, en el registro de solicitudes, **al 100 % tráfico de negocio**. Por eso las estadísticas de sesión quedan unos **7.021,91 $ (79 %)** por debajo del panel (base de cálculo sin sondeo). **Las cifras del panel (a nivel de solicitud) nunca se vieron afectadas** ⇒ para una conciliación exacta de costes use el panel, o ponga la página de conciliación en la base «negocio» antes de comparar.
2. **Sesiones que cruzan la ventana (E)**: 18 sesiones cuya primera solicitud es anterior a la ventana siguen activas dentro de ella y hoy se excluyen por completo, lo que deja fuera 3.191 solicitudes / 281,38 $ (un 3,2 % del coste del panel).
3. **Llamadas sin sesión asociada (F)**: 381 llamadas directas a la API o desde scripts no tienen sesión; el panel las cuenta, las estadísticas de sesión no.
4. **Retraso del mismo día en la instantánea de conciliación**: la conciliación es de base T+1, **los datos de hoy están incompletos**; restarlos directamente de las cifras en vivo del panel produce siempre una diferencia.
5. La tarjeta de créditos del panel no cambia con la ventana temporal (véase arriba).
6. **Alcance de sonda en estado degradado**: cuando los gráficos circulares y la tendencia del panel recurren a las tablas preagregadas tras fallar la ruta principal, esas tablas son siempre «sin sondas» ⇒ en modo «con sondas» discrepan del resumen principal (la carga útil lleva entonces una marca `degraded`: no es una degradación silenciosa).
