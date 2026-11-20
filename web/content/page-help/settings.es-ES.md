# Explicación de los datos de la página de configuración del sistema

Esta página es el «panel de control general» de toda la plataforma: aquí se encuentran la configuración de la interfaz, el comportamiento de la compresión, los interruptores de seguridad y otras opciones globales.

## Cómo se aplican los cambios de configuración

- La mayoría de las opciones **surten efecto al guardarlas** (la siguiente solicitud se ejecutará con los nuevos valores).
- Las opciones marcadas como «Requiere reinicio» deben esperar a que se reinicie el servicio; la página muestra un aviso específico para este tipo de opciones.

## Modos sencillo/experto

- «Sencillo»: la página de inicio muestra la lista de integraciones y todas las funciones están agrupadas en «Avanzado», pensado para quienes no las utilizan a menudo.
- «Experto»: navegación completa y acceso a todas las páginas.
- Esto solo cambia la **proyección de la interfaz**: ambos modos muestran los mismos datos y utilizan los mismos criterios de cálculo, por lo que las cifras no varían según el modo.

## Modo de compresión (gestión de conversaciones demasiado largas)

- Modo 1 (predictivo): comprime de forma preventiva cuando se acerca al límite de capacidad del modelo.
- Modo 2 (reactivo, recomendado): tras recibir un error de «longitud excesiva», comprime y reintenta automáticamente: comprime solo cuando es necesario para ahorrar recursos.
- La compresión solo afecta a **la organización del contenido enviado al modelo**, no elimina el texto original de la conversación; las estadísticas y la facturación se calculan según las llamadas realmente realizadas.

## Cómo comprobar las discrepancias entre las cifras

1. Has cambiado una opción, pero el comportamiento no ha cambiado: comprueba primero si está marcada como «Requiere reinicio» y después si al guardar se ha mostrado una confirmación de éxito.
2. Las cifras difieren entre los dos modos: no es posible. Si observas esta diferencia, significa que el periodo temporal o los filtros no coinciden; alinea primero el periodo.
3. El interruptor está activado, pero algunas solicitudes no lo siguen: algunos interruptores se aplican por organización o clave, mientras que los interruptores globales solo actúan como respaldo. Para una solicitud concreta, prevalece la configuración más específica.
