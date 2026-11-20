# Explicación de los datos de la página de operaciones de enrutamiento AUTO

Esta página es el espacio de trabajo de circuito cerrado del «ajuste automático de parámetros»: etiquetado → estadísticas → recomendaciones de perfiles → aprobación de ajustes de parámetros. Con este proceso, el sistema cada vez sabe elegir mejor el modelo, pero **cada aplicación debe superar las validaciones y recibir aprobación humana**.

## Cuatro pestañas forman un flujo de trabajo

- **Espacio de trabajo de etiquetado**: el personal etiqueta manualmente las solicitudes históricas según si el resultado es bueno o malo, y estos datos son el material de partida para todas las estadísticas posteriores.
- **Estadísticas de etiquetado**: resumen del volumen de etiquetado (pendientes de hoy, total acumulado y número de participantes).
- **Perfil de tareas**: muestra el rendimiento de los modelos por tipo de tarea y genera recomendaciones de perfil como «este tipo de tarea funciona mejor con este modelo».
- **Aprobación de ajustes de parámetros**: aquí las propuestas de ajuste de parámetros formuladas por el sistema esperan la aprobación o el rechazo de una persona.

## Criterios de cálculo

- «Pendientes de etiquetado de hoy» se calcula por día natural (UTC); lo no etiquetado del día anterior se acumula en las tareas pendientes de «Etiquetado acumulado».
- Las propuestas de ajuste de parámetros tienen un umbral de confianza: el sistema solo inicia el proceso de aprobación de las recomendaciones en las que tiene confianza; las de baja confianza quedan en espera automáticamente y no requieren la atención del personal de aprobación.
- Antes de aplicar cada ajuste de parámetros, se guarda una «instantánea del valor anterior»: si, tras la aprobación, se detecta que el resultado no es el esperado, se puede revertir con un clic al estado anterior al ajuste.

## Cómo comprobar las discrepancias cuando las cifras no coinciden

1. Es normal que el número de etiquetados no coincida con el de solicitudes: no todas las solicitudes necesitan etiquetado y el sistema solo selecciona una muestra representativa.
2. Una propuesta rechazada no volverá a mostrar la misma recomendación: las recomendaciones de la misma familia tienen en cuenta las decisiones de aprobación anteriores.
3. Si las métricas no cambian después de aplicar un ajuste de parámetros: la aplicación de los parámetros tiene un retardo (intervalo de recarga en caliente) y el efecto solo se refleja con el tráfico nuevo.
4. Si un enlace profundo no abre la pestaña correcta: las cuatro páginas antiguas de esta sección se han integrado en una sola página con cuatro pestañas y los enlaces antiguos redirigen automáticamente.
