# Explicación de los datos de la página del ciclo de vida de los datos

Esta página gestiona el «almacenamiento, la migración y la limpieza» de los datos: qué datos ocupan espacio en la base de datos, cuándo se archivan y cómo se liberan.

## Resumen de pestañas

- **Resumen de almacenamiento**: cuánto espacio ocupa cada tabla y cuánto crece.
- **Migración de tablas Hot y limpieza de particiones**: las tablas grandes con muchas escrituras se almacenan en divisiones mensuales y los meses anteriores se archivan íntegramente.
- **Restauración tras degradación y limpieza**: los datos agregados se reconstruyen a partir de los detalles cuando se损坏.
- **Configuración del almacenamiento**: conexión a la base de datos y configuración de la ubicación de almacenamiento.
- **Gestión de registros**: retención y limpieza de los registros de ejecución.
- **Adjuntos de sesión / Campos grandes / Sistema de archivos**: ocupación y mantenimiento de los datos basados en archivos.

## Criterios clave

- **Periodo de retención**＝tiempo que los datos se conservan en la base de datos; al cumplirse el plazo, pasan a ser «eliminables» y, antes de limpiarlos, se puede previsualizar el número de filas afectadas.
- «Eliminable» no significa «eliminado»: la limpieza requiere una previsualización y una ejecución manual; si se escriben datos nuevos entre la previsualización y la ejecución real, el número de filas puede variar ligeramente.
- El espacio ocupado por las tablas primero aumenta y luego disminuye: durante las operaciones de organización (recuperación de espacio), el disco se duplica primero (los archivos antiguos y los nuevos coexisten) y el espacio solo se libera al finalizar.

## Mecanismos de seguridad

- Todas las acciones de limpieza requieren «previsualizar» y después «ejecutar»; son dos pasos separados.
- Archivar consiste en mover los datos al almacenamiento lento, no en eliminarlos; los datos archivados todavía se pueden recuperar.

## Cómo comprobar las discrepancias numéricas

1. Las cifras de ocupación proceden de las estadísticas de la base de datos y presentan una diferencia fija respecto al tamaño de los archivos observado en el sistema operativo (la base de datos tiene espacio de redundancia interna); basta con que el orden de magnitud coincida.
2. El aumento del número de filas «eliminables» con el tiempo es normal (envejecimiento de los datos), no un crecimiento anómalo.
3. Las variaciones en el número de filas observadas durante una reconstrucción o migración son un estado intermedio; prevalecen las cifras finales una vez completada la tarea.
4. Primero compruebe la pestaña: la misma tabla tiene criterios distintos en cada pestaña (el resumen muestra la ocupación y la página de particiones, la distribución de filas por partición).
