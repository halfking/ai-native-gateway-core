# Información sobre los datos de la página de lista de claves

Esta página gestiona las «claves» de la pasarela: cada clave representa una identidad que puede iniciar llamadas a modelos.

## Campos de cada fila

- Nombre y prefijo: identificador legible de la clave; la clave completa solo se muestra una vez, en el momento de su emisión.
- Estado: consulte los cuatro estados que se explain a continuación.
- Consumo y cuota: porcentaje de consumo respecto de la cuota asignada.

## Cómo se determinan los cuatro estados

| Estado | Condición |
| --- | --- |
| Pendiente de activación | La clave se ha emitido, pero todavía no ha completado la primera validación |
| Activada | El estado es normal + no ha caducado + no se ha desactivado; las tres condiciones deben cumplirse simultáneamente |
| Caducada | Tiene una fecha de expiración y ya ha pasado |
| Revocada | Se ha desactivado manualmente |

Nota: «Activada» requiere que las tres condiciones se cumplan al mismo tiempo. Si la clave ha caducado o se ha desactivado, la página mostrará «Caducada» o «Revocada» aunque el campo de estado del sistema siga siendo «Normal».

## Criterios de cálculo del consumo

- La cifra de consumo cuenta las **llamadas completadas**; las solicitudes en curso no se incluyen.
- Los datos de consumo tienen un breve retraso: una llamada recién iniciada puede tardar un poco en reflejarse en el consumo.

## Cómo comprobar las discrepancias numéricas

1. Qué tipo de pestaña de filtro por estado está seleccionado: la página tiene pestañas por estado en la parte superior y el contenido de la lista cambia al cambiar de pestaña.
2. La caducidad se determina según la hora del servidor; cualquier diferencia en el reloj del equipo no afecta a la determinación.
3. Las claves integradas en el sistema (las que utiliza la propia plataforma) aparecen siempre en la lista y no se incluyen en las estadísticas generales.
4. Las cuotas y el consumo proceden de dos fuentes de datos distintas y se actualizan con frequencies diferentes: el consumo se refleja casi en tiempo real y los cambios de cuota se aplican inmediatamente. Si no coinciden, actualice primero la página.
