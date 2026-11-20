# Página de auditoría de reescritura de rutas

Registro de auditoría de quién cambió la configuración de las rutas y cuándo.

## Criterios de cálculo

Cada registro incluye los valores anteriores y posteriores al cambio y la persona que lo realizó; los registros solo se añaden y nunca se modifican.

## Cómo comprobar las discrepancias cuando los números no coinciden

- 1.No se encuentra un cambio: se modificó directamente la base de datos sin pasar por la página, por lo que no queda ningún registro de auditoría; cualquier operación realizada en la página genera un registro.
- El comportamiento no cambia tras la modificación: confirme el rango de tráfico al que se aplica realmente la regla.
