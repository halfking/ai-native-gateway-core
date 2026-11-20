# Página de opciones del filtro de rondas

Mantenimiento de las opciones del filtro de la lista de rondas (valores desplegables como proyecto/cliente).

## Criterios de definición

Opción = valores que aparecen realmente + registros manuales; se combinan y se eliminan los duplicados.

## Cómo comprobar las discrepancias cuando los números no coinciden

- 1. No se encuentra un proyecto al filtrar: si su nombre no está registrado y hay tráfico nuevo, aparece automáticamente; también se puede registrar manualmente.
- Las opciones no coinciden con la lista: actualiza la caché de opciones y vuelve a intentarlo.
