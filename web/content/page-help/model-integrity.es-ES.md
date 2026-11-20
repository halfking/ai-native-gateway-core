# Explicación de los datos de la página de supervisión de la integridad de los modelos

Esta página controla si «las respuestas entregadas por el proveedor son completas y se han sustituido sin avisar»: las sustituciones, los truncamientos, las respuestas vacías, las repeticiones y la deriva de la huella digital se consideran anomalías de integridad.

## Dos pestañas

- **Eventos de integridad**: cada anomalía tiene un registro (tipo, gravedad, modelo/proveedor implicado y si está resuelto).
- **Deriva de la huella digital**: cambios en la «huella digital de comportamiento» de un mismo modelo a lo largo del tiempo: una deriva elevada indica que el proveedor ha cambiado discretamente su implementación.

## Criterios de cálculo

- El recuento de eventos / no resueltos / graves se calcula según la ventana estadística seleccionada; la ventana se puede cambiar.
- «Grave» = anomalías que afectan a la corrección o la seguridad de la respuesta (como la sustitución de modelos); «truncamiento/respuesta vacía» suele considerarse de gravedad media.
- La deriva de la huella digital es una comparación **respecto a la línea base**: cuando se actualiza la línea base, se recalculan las conclusiones históricas sobre la deriva.

## Cómo comprobar las discrepancias cuando los números no coinciden

1. El número de eventos difiere del de la página de anomalías de formato: ambas páginas se centran en distintos tipos de anomalía (esta página gestiona la «integridad de las respuestas» y la otra, el «formato de la respuesta»); hay solapamiento, pero no coinciden.
2. El número de «no resueltos» no disminuye: es necesario confirmar manualmente la resolución; como en la página de anomalías de formato, la recuperación automática no cierra automáticamente el caso.
3. De repente, todas las huellas presentan deriva: compruebe primero si se acaba de actualizar la línea base o de cambiar la ventana estadística y solo después sospeche que todos los proveedores han cambiado su implementación a la vez.
