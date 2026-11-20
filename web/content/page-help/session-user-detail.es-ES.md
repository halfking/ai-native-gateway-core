# Explicación de los datos de la página de detalles de sesiones de un usuario

Esta página muestra el perfil de uso completo de **una persona**: sus sesiones, proyectos, tareas, modelos más utilizados y tendencias de gasto.

## Composición del contenido

- **Tarjeta de resumen**: número de sesiones, Token, gasto, tasa de éxito, tasa de aciertos de caché y duración media.
- **Zona de tendencias**: curvas diarias de sesiones/gasto dentro del periodo seleccionado para este usuario.
- **Tabla de detalle**: filas de uso desglosadas por proyecto / tarea / agente / modelo.

## Criterios de cálculo

- Todas las cifras solo cuentan las sesiones **iniciadas por ese usuario**; la participación en colaboraciones (cuando aparece en las sesiones de otra persona) no se cuenta.
- «Gasto» se muestra con dos criterios de cálculo en paralelo; la tarjeta «Clasificación» muestra la posición de este usuario en la clasificación general y su variación respecto al periodo anterior.

## Cómo comprobarlo cuando las cifras no coinciden

1. Total de detalle ≠ cifra de la fila en la página de lista: primero alinea los intervalos de tiempo de ambas páginas (la parte superior de la página de detalle tiene su propio selector de intervalo).
2. Total de los detalles por proyecto/tarea ≠ total del resumen: las sesiones no asignadas a ningún proyecto/tarea solo se incluyen en el total; si hay una fila «Sin clasificar» al final de la tabla de detalle, utilízala para explicar la diferencia.
3. La dirección de la «variación de clasificación» está invertida: la variación se calcula según la **posición en la clasificación** (subir = mejor posición), no según el aumento o descenso del valor.
