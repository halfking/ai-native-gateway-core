# Explicación de los datos de la página de registros de auditoría

Esta página registra «quién cambió qué y cuándo»: es el historial de las operaciones de gestión y se utiliza para el rastreo posterior.

## Cada registro contiene

Persona responsable, fecha y hora, tipo de acción (por ejemplo, modificar la configuración, desactivar una clave o ajustar parámetros), objeto afectado y valores anteriores y posteriores al cambio.

## Alcance de los registros

- Se registran únicamente las **operaciones de administración** (las modificaciones realizadas desde la página de administración interna o la API de mantenimiento); las llamadas al modelo de los usuarios habituales no se registran aquí: consulte la página de registros de solicitudes.
- Las operaciones de lectura (consultar sin modificar) no se registran de forma predeterminada.

## Técnicas de consulta

- La combinación «persona responsable + intervalo de tiempo» es la más eficaz: primero localice a la persona y después acote el intervalo de tiempo.
- Los «valores anteriores y posteriores al cambio» constituyen la fuente autorizada para comprobar «cuándo cambió la configuración y quién lo hizo».

## Qué hacer si las cifras no coinciden

1. Ha modificado la configuración pero no encuentra ningún registro: compruebe que la operación se realizó a través de la interfaz de administración o de la API de mantenimiento (las vías que cuentan con controles de auditoría). Si se modifica directamente la base de datos sin pasar por la pasarela del sistema, no quedará constancia.
2. Si hay una diferencia de varias horas: la hora de los registros es la hora universal coordinada (UTC); convierta manualmente la hora local (la hora de Pekín es UTC+8).
3. La misma acción aparece en dos registros: cada operación secundaria genera su propio registro (por ejemplo, «Actualizar la configuración» puede dividirse en dos: leer el valor anterior y escribir el nuevo). Tome como referencia el registro más completo.
