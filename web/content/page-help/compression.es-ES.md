# Explicación de los datos de la página de gestión de la compresión

Esta página supervisa la «reducción de las conversaciones demasiado largas»: cómo comprime el sistema una conversación cuando es demasiado larga para caber en la capacidad del modelo.

## Indicadores clave

- **Número de sesiones comprimidas**: Número de sesiones en las que se ha activado la compresión.
- **Tasa de compresión**: Porcentaje de contenido que se ha eliminado tras la compresión.
- **Distribución de estrategias**: Número de veces que se ha utilizado cada estrategia de compresión (preventiva/reactiva/ventana deslizante/incremental, etc.).
- **Tendencia de la tasa de compresión**: Evolución diaria de la tasa media de compresión.

## Diferencias entre los dos tipos de activación

- **Compresión preventiva** (preliminar): Comprime de forma anticipada cuando se estima que la conversación está a punto de superar la capacidad, sin que el usuario perciba ningún cambio.
- **Compresión forzada**: Solo se comprime y se reintenta después de que el servicio de destino rechace la solicitud (error de capacidad insuficiente). Esto supone el coste adicional de una llamada fallida, pero solo se activa cuando es necesario.

## Criterios de cálculo

- La compresión ocurre «antes de enviar la solicitud al modelo», por lo que el texto original de la sesión no se modifica; se mide la proporción en que se reduce el contenido enviado al servicio de destino.
- Una ronda de conversación puede activar la compresión varias veces (a medida que se prolonga, se recorta repetidamente), y cada activación se contabiliza por separado.

## Cómo comprobar las discrepancias en los números

1. Una tasa de compresión anormalmente alta, cercana al 100%: Compruebe si se ha configurado correctamente el umbral de compresión (si se han comprimido también consultas muy pequeñas).
2. Es normal que el número de «sesiones comprimidas» sea mucho mayor que el de «compresiones forzadas»: La mayoría se resuelven de forma preventiva y solo las que se pasan por alto requieren compresión forzada.
3. Si la configuración muestra «Valor predeterminado de fábrica»: La parte superior de la página avisará de que no se ha podido leer la configuración. En ese caso, los valores que se muestran a continuación no reflejan la configuración real actual: primero resuelva el error de lectura y después ajuste los parámetros.
