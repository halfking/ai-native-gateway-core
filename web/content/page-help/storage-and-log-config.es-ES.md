# Configuración de almacenamiento y registros

Tres resúmenes de solo lectura: configuración actual del backend de almacenamiento, el mantenimiento del sistema de archivos y la gestión de registros.

## Criterios de referencia

Visualización de solo lectura = esta página no modifica la configuración; los cambios se realizan en la subpágina correspondiente.

## Cómo comprobarlo si las cifras no coinciden

- 1.El valor mostrado difiere de la variable de entorno: la variable de entorno tiene prioridad sobre la configuración de la base de datos; prevalecerá la fuente indicada en la página.
- La página no se actualiza después de realizar un cambio: esta página tiene una caché; recarga una vez.
