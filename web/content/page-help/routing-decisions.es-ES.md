# Página de decisiones de enrutamiento

Registro del resultado final de enrutamiento de cada请求 (quién se seleccionó y por qué).

## Criterios de cálculo

Una fila = una decisión; incluye la cadena de reglas coincididas y el candidato final.

## Cómo comprobarlo cuando no cuadran los números

- 1.Número de decisiones ≠ número de solicitudes: las solicitudes bloqueadas previamente no entran en el proceso de enrutamiento y no generan ningún registro de decisión.
- Dos decisiones para la misma solicitud: la llamada principal y el reintento generan una decisión cada una.
