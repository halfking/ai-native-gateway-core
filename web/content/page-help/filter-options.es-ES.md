# Página de opciones de filtrado

Mantiene las opciones disponibles en los filtros de las distintas páginas de listados (modelos, claves, tenants, etc.).

## Criterios de referencia

Origen de las opciones = valores que han aparecido realmente + lista blanca mantenida manualmente; ambas fuentes se combinan y se eliminan los duplicados.

## Cómo comprobar las discrepancias numéricas

- 1. No se puede filtrar un valor en la página de listados: comprueba primero aquí si la opción está registrada; los valores dinámicos aparecen automáticamente según los datos.
- Una opción eliminada vuelve a aparecer: hay tráfico real que está generando ese valor, por lo que la eliminación solo afecta a la parte manual.
