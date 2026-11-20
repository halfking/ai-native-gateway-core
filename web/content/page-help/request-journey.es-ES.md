# Detalles de la página del recorrido de la solicitud

Consulta el «recorrido» de una solicitud a lo largo de varios intentos: la entrada, la salida y el resultado de cada intento candidato se encadenan para formar una traza completa.

## Criterios de cálculo

Recorrido = solicitud principal + todos los intentos de reintento; duración total = diferencia entre las horas de inicio y fin, incluidos todos los intentos.

## Qué revisar cuando no cuadran los números

- 1.Número de recorridos ≠ número de candidatos: se incluyen los reintentos internos y los mecanismos de repliegue; la facturación se basa en el intento exitoso final.
- Intento interrumpido a mitad: los intentos que agotan el tiempo de espera del sistema upstream se etiquetan como «Interrumpido»; el coste de la ejecución parcial se determina según los criterios de facturación reales del sistema upstream.
