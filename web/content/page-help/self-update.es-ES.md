# Datos de la página de autoactualización

Esta página gestiona las actualizaciones de versión de la propia pasarela: consultar la última versión → generar un aviso → instalar después de que el administrador lo confirme.

## Tres estados

- **Fuente de versiones no configurada**: este nodo no ha indicado «dónde buscar nuevas versiones», por lo que las comprobaciones automáticas no están habilitadas.
- **Estado de versión desconocido**: se ha consultado la fuente, pero no se ha obtenido un resultado (problema de red o fuente inaccesible).
- **Normal**: se conocen la versión actual y la última versión, por lo que se pueden comparar.

## Proceso de actualización

1. «Comprobar actualizaciones ahora» consulta el número de versión más reciente en la fuente de versiones configurada.
2. Cuando hay una versión nueva, se genera un aviso; **no se instala automáticamente**.
3. Cuando el administrador confirma la instalación, se inicia el proceso de actualización y, al finalizar, el servicio se reinicia automáticamente con la nueva versión.

## Criterios de referencia

- La «versión actual» es la versión **que está ejecutando este nodo**; en despliegues multinodo, cada nodo puede tener una versión diferente y esta página solo muestra la de este nodo.
- Durante la actualización, el servicio se reinicia brevemente: mientras tanto, las solicitudes las atienden otros nodos (en despliegues multinodo) o se interrumpen brevemente (en despliegues de un solo nodo).

## Cómo comprobar las discrepancias cuando los datos no coinciden

1. Si acaba de actualizar y aún se muestra la versión anterior: es necesario refrescar una vez la información de versión almacenada en caché de la página para que se actualice.
2. Si la «última versión» no coincide con la página oficial de publicación de versiones: esta página consulta la **fuente de versiones configurada**; cuando no esté sincronizada con la fuente oficial, prevalece la fuente configurada. Primero, compruebe la dirección de la fuente de versiones.
3. Si la comprobación de actualizaciones muestra siempre «desconocido»: la dirección de la fuente de versiones no es accesible; pruebe la conectividad con esa dirección directamente desde el equipo de despliegue.
