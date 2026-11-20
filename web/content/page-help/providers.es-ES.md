# Página de lista de proveedores: explicación de los datos

Esta página gestiona los proveedores upstream conectados (OpenAI, Claude y otros servicios de modelos), así como las credenciales y los modelos de cada proveedor.

## Diferencia entre los dos estados

- **Salud del sistema**: disponibilidad a nivel de credencial (normal/advertencia/inaccesible/desconocido), a partir de las pruebas de detección y de los registros de fallos del tráfico real.
- **Enrutabilidad**: indica si el proveedor **participa actualmente en la selección de ruta** (disponible/no disponible/sin modelos/desactivado manualmente). Un proveedor puede estar sano y, aun así, estar desactivado manualmente; en ese caso no recibe tráfico, pero los datos de salud siguen actualizándose.

## Relación entre modelos y credenciales

- Un proveedor puede tener varias credenciales y dar servicio a varios modelos; un problema en cualquier credencial solo afecta a los modelos que sustenta.
- «Sin modelos» = el proveedor está conectado, pero todavía no tiene configurado ningún modelo disponible, por lo que no recibe tráfico.

## Ordenación y filtrado

- De forma predeterminada, se ordena por estado de salud. El filtro de disponibilidad (enrutable/no enrutable) es un interruptor independiente: no se afectan entre sí y solo determinan qué filas se muestran.

## Cómo comprobar las discrepancias en los números

1. Está sano pero no recibe tráfico: comprueba primero la «enrutabilidad»; los proveedores desactivados manualmente no participan en la selección de ruta.
2. El número de modelos no coincide con los realmente seleccionables: la lista de modelos se filtra según la disponibilidad de las credenciales; cuando todas las credenciales están fuera de servicio, los modelos desaparecen temporalmente de las opciones.
3. Una credencial recién guardada muestra «En detección»: tras guardarla, el sistema inicia automáticamente una comprobación de salud y, hasta que hay resultado, se muestra un estado transitorio.
4. El filtro se recuerda al actualizar: la página conserva las condiciones de filtrado de la última vez; cuando otra persona use la página, debe borrar primero los filtros.
