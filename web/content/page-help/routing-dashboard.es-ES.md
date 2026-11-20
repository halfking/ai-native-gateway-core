# Explicación de los datos de la página de resumen de enrutamiento

Esta página responde a «por qué se ha asignado una solicitud a un modelo»: te muestra el proceso de selección de ruta desglosado.

## Conceptos clave

- **Candidatos**＝credenciales que, tras aplicar los criterios de filtrado, pueden atender una solicitud.
- **Enrutables**＝parte de los candidatos realmente disponible en este momento (en buen estado, no deshabilitados y fuera del periodo de enfriamiento).
- **Registro de decisiones**＝historial cronológico de los resultados de enrutamiento de cada solicitud real.

## Criterios estadísticos

- El «número enrutable» se determina en el momento actual: un mismo candidato puede estar disponible un minuto y entrar en enfriamiento al minuto siguiente, por lo que la cifra cambia.
- Los detalles de las decisiones muestran los candidatos por «nivel», en orden: los primeros tienen prioridad para atender la solicitud; si uno falla, se pasa al siguiente en orden.
- **Respaldo**＝cambio al siguiente candidato después de que falla el preferido; una solicitud puede tener, como máximo, un único registro final de éxito.

## Descripción de las pestañas

- Decisiones en tiempo real: ejecuta una simulación para ver «cómo se elegiría una solicitud si llegara ahora».
- Supervisión de credenciales / Comprobación de estado: detalles sobre la disponibilidad de las credenciales de los proveedores de origen.
- Operaciones automáticas: herramienta de ajuste automático de parámetros y análisis del sistema.

## Cómo comprobar las discrepancias numéricas

1. El resultado de la simulación no coincide con el registro real: la simulación es una «proyección del momento actual», mientras que la decisión real se tomó en el pasado; el estado de enfriamiento y la desactivación temporal pueden provocar diferencias.
2. El número de decisiones no coincide con el de la página de registros de solicitudes: la página de decisiones solo registra los eventos de enrutamiento; las solicitudes que no llegaron a entrar en el proceso de enrutamiento, como las interceptadas previamente, no aparecen aquí.
3. Hay «enrutables = 0», pero la solicitud se completó: esto indica que el sistema recurrió a un respaldo entre proveedores o a un canal de respaldo; la cadena de alternativas se puede consultar en los detalles de la decisión.
4. Diferencia temporal: la expiración del enfriamiento y la recuperación de las credenciales cambian en cuestión de minutos; si las dos consultas se realizan con varios minutos de diferencia, los resultados serán distintos.
