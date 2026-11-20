# Explicación de los datos de la página de lista de usuarios por sesión

Esta página muestra el uso por «persona»: cada usuario ocupa una fila, con el total de sus sesiones, Token, coste y modelos más utilizados.

## Campos de cada fila

Usuario (nombre visible/cuenta), número de sesiones, número de Token, coste, tasa de éxito y lista Top de modelos más utilizados (consultar al expandir la fila).

## Relación con el resumen de análisis de sesiones

- La página de resumen agrega todos los datos; esta página desglosa los mismos datos **por persona**. Si el periodo temporal es el mismo, ambas páginas coinciden.
- La página de clasificación es una vista ordenada de esta página (Top N); esta página contiene la lista completa y permite buscar.

## Criterios de cálculo

- La columna «Coste» muestra dos criterios de facturación en paralelo, sin convertirlos entre sí.
- Las sesiones de un usuario se atribuyen al «iniciador»: el uso de varios participantes dentro de una misma sesión se registra a nombre del iniciador.

## Cómo comprobar las discrepancias cuando los números no coinciden

1. El número de usuarios supera el de los usuarios del sistema: esta página cuenta las identidades que «han tenido actividad en alguna sesión» (incluidas las cuentas de sistema/servicio); para consultar el directorio de personas, vaya a la página de lista de usuarios.
2. El total de uso de modelos de la fila expandida ≠ el total de Token de la fila: la lista expandida solo incluye los modelos Top; los modelos de cola larga se agrupan en «Otros».
3. La misma persona aparece en dos filas: los cambios de cuenta y nombre visible generan dos identidades históricas; el uso se atribuye por cuenta, por lo que debe compararse usando la columna de cuenta.
