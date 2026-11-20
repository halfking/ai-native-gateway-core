# Página de estrategias de enrutamiento

Parámetros de la estrategia de selección de rutas: orden de los niveles, límite de candidatos y reglas de alternativa.

## Explicación de los criterios

La estrategia determina «cómo seleccionar»; los datos de los candidatos determinan «qué seleccionar».

## Cómo comprobarlo cuando los números no coinciden

- 1. Si se cambia la estrategia y el tráfico no varía: la estrategia solo afecta a las solicitudes nuevas; las solicitudes en curso se completan según la estrategia anterior.
- Si el comportamiento presenta anomalías después de cambiar el orden de los niveles: existen dependencias entre los niveles; después del ajuste, compruebe primero los cálculos de prueba en la página de decisiones.
