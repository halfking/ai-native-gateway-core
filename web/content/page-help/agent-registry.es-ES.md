# Página de registro de agentes

Lista de agentes automatizados registrados en el panel backend, con estado de salud y atribución.

## Criterios de clasificación

Salud = latido y llamada correcta recientes; sin conexión = ningún mensaje durante un periodo superior al ciclo de latidos.

## Cómo comprobar las discrepancias numéricas

- 1.El número de agentes no guarda relación con el número de usuarios: los agentes son cuentas de servicio y no ocupan plazas de personal.
- Dos registros del mismo agente: al reconstruir una instancia se genera un ID nuevo; el uso histórico se atribuye por ID.
- Agente con estado saludable pero llamada fallida: el estado saludable solo tiene en cuenta el latido y la sesión iniciada; para ver el fallo concreto, consulte los registros de solicitudes.
