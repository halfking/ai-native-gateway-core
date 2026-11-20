# Página de cronología de acciones de la solicitud

Cada acción de una solicitud dentro de la gateway se ordena cronológicamente (con un nivel de granularidad de acción más detallado que el árbol de etapas).

## Criterios de definición

Acción = una operación interna concreta (lectura o escritura en caché, aplicación de reglas o envío a un sistema upstream); varias acciones pueden ejecutarse en paralelo al mismo tiempo.

## Cómo comprobar las discrepancias entre los números

- 1.El número de acciones es distinto del número de etapas: una etapa contiene varias acciones, por lo que el nivel de granularidad es diferente.
- El intervalo entre marcas de tiempo es 0: las acciones de submilisegundo se muestran agrupadas; no se han perdido.
