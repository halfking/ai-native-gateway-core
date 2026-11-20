# Explicación de los datos de la página de monitorización de credenciales

Esta página monitoriza la disponibilidad de las credenciales upstream (claves de cuenta para conectar con cada proveedor de modelos) y ayuda a detectar problemas antes de que los usuarios los comuniquen.

## Descripción de las pestañas

- **Lista**: tabla general de estado con una fila por credencial (estado actual, último error y motivo de desactivación).
- **Mapa de calor**: muestra por tiempo cuándo ha tenido errores cada credencial; cuanto más oscuro es el color, más errores presenta.
- **Registro de enrutamiento**: registro cronológico de eventos por credencial (activación/desactivación/cambio).
- **Tendencia de errores**: evolución de los errores por hora, que permite comprobar si son puntuales o sostenidos.
- **Salud de las comprobaciones** (solo superadministrador): resultados de las llamadas de prueba que el sistema realiza activamente con cada credencial.

## Criterios de las etiquetas de estado

- «Correcto» = no hay fallos recientes.
- «Desactivación manual» = un administrador la ha puesto fuera de servicio; las comprobaciones automáticas no la reactivarán.
- «Control automático» = el sistema la ha desactivado automáticamente por fallos consecutivos (por ejemplo, después de confirmar el fallo varias veces); un administrador puede forzar manualmente su desactivación o esperar a que se recupere automáticamente.
- La insignia del motivo de desactivación tiene prioridad sobre el estado de salud: cuando una credencial tiene tanto errores como un motivo de desactivación, la página muestra el motivo de desactivación.

## Puntuación de salud y número de errores

- El número de errores cuenta las **llamadas fallidas completadas** y se acumula dentro de la ventana temporal seleccionada.
- Los fallos de una misma solicitud solo se imputan a la credencial utilizada en ese momento; que un reintento tenga éxito no elimina el fallo registrado.

## Cómo comprobar las discrepancias cuando los números no coinciden

1. Ventanas temporales distintas: los filtros de esta página deben alinearse con la ventana del panel antes de comparar.
2. El tráfico de «comprobación» son llamadas de verificación que el sistema inicia por su cuenta y se contabiliza por separado del tráfico de los usuarios. La disponibilidad de una credencial debe evaluarse combinando las comprobaciones y el tráfico real.
3. Cuando una credencial se acaba de recuperar, el número de errores puede seguir mostrando durante un breve tiempo el valor anterior: los gráficos se agregan por hora, por lo que los resultados completos de la hora actual no estarán disponibles hasta la siguiente.
4. Si la lista está vacía, compruebe primero los filtros: la combinación de filtros por tipo de credencial, tenant y estado puede haber dejado los resultados sin coincidencias.
