# Explicación de los datos de la página de reglas de enmascaramiento

Esta página gestiona la biblioteca de reglas del «bloqueador de información sensible»: qué patrones del contenido enviado al modelo deben sustituirse u ocultarse (números de teléfono, números de documento de identidad, series de claves, etc.).

## Dos pestañas

- **Reglas**: lista de reglas (cada una define cómo se identifica y sustituye un patrón sensible).
- **Coincidencias y revisión**: registros reales de bloqueo: en qué solicitud y qué posición se produjo la coincidencia, qué regla coincidió y con qué se sustituyó el contenido.

## Criterio de aplicación de las reglas

- Cada regla se puede activar o desactivar por «modo de ejecución»; la opción global «Activado» funciona como interruptor general: si se desactiva, ninguna regla surte efecto.
- Los cambios en las reglas **se aplican de inmediato** a las solicitudes posteriores; las solicitudes históricas ya enviadas no se procesan retroactivamente.

## Criterios de los registros de coincidencias

- Una solicitud puede coincidir con varias reglas; cada coincidencia se registra por separado.
- La «Revisión» es una fase de confirmación manual: permite comprobar si la sustitución es razonable o si se trata de un falso positivo, y sirve para seguir ajustando las reglas sin afectar a las solicitudes ya completadas.

## Autocomprobación cuando las cifras no coinciden

1. Hay información sensible, pero no se ha bloqueado: comprueba primero el interruptor general y el interruptor de la regla para ese modo; después, confirma que el patrón de la regla coincide realmente con los datos (algunas reglas solo reconocen formatos concretos).
2. Hay más coincidencias de las previstas: cada coincidencia en distintos puntos de un mismo contenido se cuenta por separado; al desglosar por «Solicitud», se obtiene el número de solicitudes distintas tras eliminar los duplicados.
3. Hay demasiados falsos positivos: en «Coincidencias y revisión», agrupa por regla para comprobar en cuál se concentran; ajusta las condiciones de coincidencia de esa regla en lugar de desactivar el interruptor general.
