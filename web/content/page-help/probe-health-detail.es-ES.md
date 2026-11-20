# Página de detalles del estado de las sondas

Ficha completa de un único elemento sondeado: tasa histórica de éxito de los sondeos, distribución de latencia y detalle de los fallos.

## Criterios de cálculo

Sondeo = llamada de prueba iniciada por el sistema; la tasa de éxito se calcula por número de sondeos y se mantiene separada del tráfico empresarial.

## Cómo comprobar las discrepancias cuando los números no coinciden

- 1.Todos los sondeos pasan, pero el servicio falla: la ruta del sondeo y la ruta real pueden ser diferentes (p. ej., solicitudes simplificadas); use el tráfico real como referencia.
- La latencia no coincide con la de las solicitudes registradas: los sondeos utilizan solicitudes fijas y pequeñas, por lo que la latencia es naturalmente menor.
