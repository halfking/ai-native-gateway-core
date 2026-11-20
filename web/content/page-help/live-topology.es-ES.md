# Explicación de los datos de la página de topología en tiempo real

Esta página es el «mapa de tráfico en tiempo real» de la pasarela: qué nodos están en línea y hacia dónde fluyen las solicitudes en este momento.

## Elementos del gráfico

- **Nodo** = un punto de servicio que puede atender solicitudes (nodo de acceso o nodo de proveedor).
- **Enlace** = la línea que conecta dos nodos; su grosor o cantidad representa el volumen de tráfico.
- Una solicitud entra por el «lado del usuario», la pasarela selecciona una ruta y sale por un «enlace de proveedor» para obtener la respuesta.

## Estado de los nodos

- El estado en línea/fuera de línea procede de los latidos en tiempo real de los nodos; si Redis presenta una anomalía, el estado de salud se degrada y se muestra como «Desconocido»: esto indica un problema del canal de recolección y no que el nodo no esté realmente disponible.
- La insignia «Conexión directa interna» indica que la plataforma mantiene la conexión directa con ese nodo de proveedor, sin utilizar un proxy de terceros.

## Información de actualización en tiempo real

- La página recibe actualizaciones mediante una conexión persistente y, en condiciones normales, se refresca en pocos segundos.
- Durante una desconexión breve, la página se reconecta automáticamente; las solicitudes de ese periodo no se pierden, solo la vista se completa con retraso.
- «Se han ocultado N solicitudes que pasan por nodos no válidos» = las solicitudes que se considera que pasan por nodos no válidos no se muestran de forma predeterminada en los enlaces; si es necesario, puede activar el interruptor de visualización.

## Cómo comprobar las discrepancias numéricas

1. La vista en tiempo real es una captura del «momento actual» y no es directamente comparable con los totales acumulados de la página de estadísticas durante un «periodo pasado»; para conciliar los datos, utilice la página de registros de solicitudes.
2. Las cifras de los enlaces corresponden al número de solicitudes que han pasado por ellos dentro de una ventana móvil; como esta ventana se desplaza con el tiempo, las cifras bajan de forma natural.
3. Si observa que una solicitud permanece parada en un enlace, compruebe primero su etiqueta de estado: En curso/Finalizada/Fallida representan distintas fases.
4. Si todos los nodos muestran «Desconocido», lo más probable es que exista una avería en el canal de mensajes; actualizar la página o esperar a que se restablezca la conexión debería permitir recuperar el estado.
