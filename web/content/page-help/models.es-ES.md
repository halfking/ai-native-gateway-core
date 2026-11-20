# Explicación de los datos de las páginas de modelos y catálogo

Esta página mantiene el «diccionario de modelos»: qué modelos admite la plataforma, sus nombres canónicos y cómo se corresponden con los nombres que utiliza cada proveedor.

## Las tres pestañas

- **Estándar** (visible para las operaciones de la plataforma): lista unificada de los modelos canónicos aprobados por la plataforma: aquí se mantienen los cambios de nombre, la categorización, la longitud de contexto y las etiquetas de capacidades.
- **Directorio**: permite consultar los modelos originales y las tarifas de cada proveedor.
- **Mapeo de nombres**: un mismo modelo puede tener nombres distintos en cada proveedor (por ejemplo, con un sufijo de versión o de fecha); la tabla de mapeo alinea los «nombres de los proveedores» con los «nombres canónicos de la plataforma». Todos los informes estadísticos se agregan por nombre canónico, por lo que cuanto más completo esté el mapeo, más precisos serán los informes.

## Importancia de los nombres canónicos

- Todas las estadísticas de uso, las conciliaciones y las clasificaciones utilizan el «nombre canónico» como clave de agregación.
- Si el modelo nuevo de un proveedor aún no tiene un mapeo, su tráfico aparecerá por separado con el nombre original (en las estadísticas se manifestará como «un modelo desconocido adicional»); al añadir el mapeo, se agregará automáticamente al nombre canónico.

## Cómo comprobar las discrepancias numéricas

1. Si en un informe aparece un nombre de modelo desconocido: primero añada el mapeo en «Mapeo de nombres» y después vuelva a consultar el informe (los datos históricos se volverán a agregar por nombre canónico).
2. Que el mismo modelo tenga precios distintos según el proveedor es normal: el directorio muestra las tarifas por proveedor; el coste de las estadísticas se calcula con la tarifa del proveedor que se haya utilizado realmente.
3. Si la longitud de contexto no coincide con la del sitio web oficial: prevalecerá la configuración del directorio de la plataforma, ya que es la base que utiliza la pasarela para aplicar realmente la truncación o la compresión.
