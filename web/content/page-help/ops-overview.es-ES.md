# Explicación de los datos de la página general de operaciones

Esta página es el «panel de control global» para varios puntos de despliegue: permite ver de un vistazo el estado del servicio central, las distintas regiones, los nodos de despliegue y la salud del plano de datos.

## Cuatro bloques

- **Instancia central**: estado de funcionamiento y versión del servicio central.
- **Distribución regional**: distribución y estado de los puntos de despliegue por zona geográfica.
- **Nodos de despliegue**: últimos registros de despliegue (fecha y hora, versión y resultado).
- **Plano de datos**: tamaño y estado de salud de las principales tablas de datos.

## Criterios de estado

- El estado de los nodos se basa en los informes periódicos que envía cada nodo; «sin conexión» significa que no se ha recibido ningún mensaje tras superar el intervalo de informe, lo que puede indicar que el nodo se ha detenido o que se ha interrumpido la red.
- La versión se determina según lo que informa cada nodo; que aparezcan varias versiones en una misma región es un estado intermedio de una actualización progresiva.

## Información sobre la actualización

- Los datos de esta página se refrescan mediante sondeos a intervalos fijos (no en tiempo real al segundo); los despliegues recién completados no aparecerán hasta que se produzca el siguiente refresco.
- El valor N de «últimos N registros» es el límite de elementos que muestra la página, no el historial completo; para consultar el historial completo, vaya a Gestión de despliegues.

## Cómo comprobar las discrepancias cuando no coinciden los números

1. Primero, confirme el momento de la consulta: existe un intervalo de sondeo entre el estado que muestra esta página y el observado en el propio nodo.
2. Si el total de una región no coincide con la suma de los nodos uno a uno: los nodos que no han informado se consideran «desconocidos» y no se incluyen en el total regional.
3. Que las versiones no coincidan no siempre indica un fallo: durante una actualización progresiva, es previsto que coexistan versiones nuevas y antiguas; una vez finalizada la actualización, deberían converger en una sola versión.
