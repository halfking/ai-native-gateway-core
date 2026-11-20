# Página de estado de la migración de almacenamiento

Tablero de progreso del traslado de datos entre capas de almacenamiento.

## Criterios de cálculo

Migración = copia por lotes + validación + cambio de sistema; el progreso se mide por lotes.

## Cómo comprobar las discrepancias cuando las cifras no cuadran

- 1.El progreso no avanza: mover una sola tabla grande puede tardar mucho; consulte los detalles por lote en lugar del porcentaje total.
- Fallo de validación: si falla un lote, puede volver a ejecutar ese lote sin afectar a los lotes completados.
