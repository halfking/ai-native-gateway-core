# Explicación de los datos de la página de chat

Esta página es la «ventana de chat de prueba» integrada: permite hablar directamente con el modelo conectado a la pasarela y es la forma más rápida de comprobar si la clave es utilizable.

## Requisitos previos

Se necesita una clave de API **que te pertenezca y esté habilitada**. Si no tienes una clave, la página te dirigirá primero a solicitar una; cuando una clave se haya desactivado o haya caducado, el chat fallará e indicará el motivo.

## Selección de modelo

- Si seleccionas «auto», la pasarela elige automáticamente el modelo adecuado (según la estrategia de enrutamiento).
- Si seleccionas manualmente un modelo, se usará el elegido; si no está disponible, ese turno fallará, pero no afectará al siguiente.
- **Los cambios de parámetros se aplican en el siguiente turno**: al cambiar la temperatura o el modelo, los nuevos valores se utilizarán desde el siguiente mensaje, mientras que el historial no cambiará.

## Correspondencia con las estadísticas

- Cada turno de chat de aquí es una llamada real y **se contabiliza en el consumo y los costes**—las pruebas también cuestan dinero.
- Los registros de chat se corresponden uno a uno con los de la página «Registros de solicitudes»: los detalles de cada turno se pueden consultar en esa página.

## Cómo comprobar las discrepancias cuando las cifras no coinciden

1. Si dos turnos de una misma conversación tienen costes muy distintos: en el modo auto, el modelo se selecciona de forma independiente en cada turno, por lo que puede que se haya elegido uno más caro o más barato.
2. Si una respuesta aparece truncada: comprueba primero el ajuste «Longitud máxima de salida» y después el límite de capacidad del modelo seleccionado.
3. Si el chat falla pero la página de la clave indica que no hay problemas: confirma que la cuota de la clave no se haya agotado y que el modelo esté disponible para tu clave.
