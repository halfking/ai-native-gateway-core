# Explicación de los datos de la página de detección de recursos gratuitos

Esta página es el «detector de modelos gratuitos»: recorre los proveedores de modelos gratuitos según las plantillas para detectar nuevos modelos disponibles.

## Los tres bloques

- **Gestión de plantillas**: define las plantillas que indican «qué proveedores se deben revisar y qué condiciones se aplican para considerar que un modelo es gratuito».
- **Tareas de detección**: registra cada tarea que activa un análisis (cuándo se realizó y cuántos modelos se detectaron).
- **Resultados de detección**: incluye la lista de modelos candidatos detectados por la tarea, pendiente de revisión manual para decidir si se incorporan al conjunto de recursos gratuitos.

## Flujo y criterios de conteo

- Activar la detección → el sistema comprueba los proveedores según las plantillas → los resultados **se revisan antes de incorporarse**: detectar un modelo ≠ que esté disponible; debe confirmarse manualmente para que entre en el conjunto de recursos gratuitos.
- El «número de detecciones» cuenta los modelos candidatos; si se detecta varias veces el mismo modelo, las detecciones se combinan y no se incorpora dos veces.
- Eliminar una plantilla no afecta las tareas ni los resultados ya generados.

## Cómo comprobar las discrepancias cuando los números no coinciden

1. La tarea indica que se detectaron 3 modelos, pero la página de resultados solo muestra 2: los candidatos duplicados se han combinado, por lo que prevalece el número de entradas tras la combinación.
2. Un modelo detectado no está disponible después de incorporarlo al conjunto: el nivel gratuito fluctúa mucho y puede que las credenciales ya hayan sido limitadas por el proveedor de origen; consulta el estado de las credenciales en la página del conjunto de recursos gratuitos.
3. La tarea permanece en «En curso»: las respuestas lentas de los proveedores pueden ampliar el tiempo de comprobación; si no termina en media hora, revisa la conexión y la configuración de las plantillas.
