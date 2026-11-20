# Explicación de los datos de la página de lista de turnos

Esta página organiza las llamadas al modelo por «Sesión → Turno»: una sesión es una conversación continua y un turno es una pregunta y su respuesta.

## Relación jerárquica

- Proyecto / Usuario / Cliente / Clave son cuatro perspectivas de agrupación; el mismo conjunto de sesiones se puede analizar desde distintos ángulos.
- Al expandir una sesión, se puede ver cada uno de sus turnos: resumen del contenido de la pregunta, resumen de la respuesta, tiempo transcurrido y estado.

## Cómo se cuentan los «turnos»

- Cada pregunta del usuario y su respuesta del modelo cuentan como un turno.
- Dentro de una misma sesión, los turnos se ordenan cronológicamente.
- Un turno puede incluir varias llamadas internas al modelo (reintentos, llamadas a herramientas); estas se incluyen en el turno y no se cuentan por separado.

## Resúmenes y estados

- El resumen de la sesión lo genera automáticamente el sistema; «Resumir ahora» activa manualmente una nueva generación y, durante el proceso, se muestra el estado de ejecución.
- Cuando la grabación no está disponible, es posible que una sesión nueva no tenga todavía el detalle de sus turnos. Se trata de un estado temporal a nivel de captura, no de pérdida de datos.

## Cómo comprobar las discrepancias numéricas

1. Esta página agrega por «Sesión», mientras que la página de registros de solicitudes muestra cada «llamada individual»: una solicitud puede ser solo parte de un turno en la vista de sesión, por lo que el número de registros de las dos páginas no coincide de forma natural.
2. Cuantas más dimensiones de filtro (proyecto/usuario/cliente/clave) se seleccionen, menos sesiones habrá: borra los filtros y vuelve a comparar el total.
3. Hay un límite de turnos cargados (cada expansión recupera los 100 turnos más recientes): para sesiones muy largas, reduce el intervalo de tiempo o consulta las páginas siguientes.
