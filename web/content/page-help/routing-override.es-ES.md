# Página de reescritura de rutas

Permite forzar manualmente una ruta: dirige el tráfico de un modelo o una clave al destino especificado.

## Criterios de aplicación

La reescritura tiene prioridad sobre la selección automática de rutas; surte efecto de inmediato y puede revocarse en cualquier momento.

## Cómo comprobar los datos cuando los números no cuadran

- 1.La reescritura no surte efecto: compruebe el alcance de coincidencia de las condiciones de reescritura (modelo/clave/inquilino); unas condiciones demasiado restrictivas no coincidirán con el tráfico.
- Después de revocarla, el tráfico sigue yendo al destino anterior: el resultado de la decisión permanece en la caché durante unos segundos y, más tarde, vuelve a seguir la ruta normal.
