# Explicación de los datos de la página de la lista de aprobaciones

Esta página centraliza los asuntos que requieren una decisión manual: algunas operaciones delicadas o solicitudes que superan el presupuesto se dejan en espera hasta que un administrador las apruebe aquí.

## Flujo de estados

Pendiente de aprobación → (Aprobado | Rechazado) → Archivado.

- Los asuntos en «Pendiente de aprobación» mantienen su estado de espera hasta que se resuelvan (por ejemplo, una solicitud permanece a la espera mientras se tramita su aprobación).
- Las aprobaciones tienen una política de tiempo de espera: las que no se tramitan dentro del plazo se aprueban o se rechazan automáticamente según la configuración, y la página de detalles indica qué política se aplica a cada una.

## Relación entre la página de configuración y la de lista

- «Configuración de aprobaciones» define **qué situaciones** requieren aprobación, quién debe aprobarlas y cuál es el plazo de espera.
- «Lista de aprobaciones» recoge las tareas pendientes **que se generan efectivamente** conforme a la configuración.
- Cambiar la configuración solo afecta a las aprobaciones nuevas que se generen después; no afecta a las aprobaciones que ya figuran en la lista.

## Cómo comprobar las discrepancias entre las cifras

1. El número de elementos de la lista no coincide con el de notificaciones: puede que las notificaciones incluyan asuntos que ya ha resuelto otra persona; utiliza como referencia la pestaña «Pendiente de aprobación» de la lista.
2. El mismo asunto aparece asociado a dos aprobaciones: cada una procede de una política distinta y la página de detalles indica el origen de la activación.
3. Se ha aprobado, pero la función no está operativa: su entrada en vigor puede tardar un breve momento; si sigue sin estar operativa después de superar el umbral, ve a la página de la función correspondiente para comprobarlo.
