# Explicación de los datos de la página de gestión de precios

Esta página gestiona la «tabla de precios de coste»: indica cuánto cuesta invocar un modelo en el proveedor de origen y sirve como base para el cálculo de costes y la fijación de precios interna.

## Descripción de las vistas

- **Vista de árbol**: permite explorar la jerarquía «Proveedor → Modelo»,_resultado ideal para revisar cada proveedor por separado.
- **Vista de tabla**: presenta todos los precios en una sola tabla, adecuada para comprobaciones masivas y exportaciones.

## Criterios de precios

- Los precios se calculan por cada millón de Token: el precio de entrada y el de salida son independientes y, normalmente, el de salida es más caro.
- La parte de la entrada que produce un acierto de caché se cobra al precio con descuento (para algunos proveedores, un 10 %); los modelos con precio de caché tienen una fila independiente.
- Los cambios de precio solo afectan a las llamadas **posteriores a su entrada en vigor**; las facturas históricas quedan congeladas con los precios vigentes en ese momento y no se recalculan retroactivamente.

## Cómo comprobar las discrepancias numéricas

1. El coste y la factura del proveedor difieren en un decimal: compruebe la unidad de cálculo (por millón frente a por mil de Token), ya que los valores difieren en un factor de 1000.
2. El mismo modelo aparece en dos filas de precios: una corresponde al precio estándar y otra al precio de caché, o bien se trata de accesos con proveedores distintos; consulte primero la columna «Proveedor».
3. El informe del día no cambia después de modificar un precio: los informes se congelan por día y el snapshot del día siguiente refleja el nuevo precio.
4. Diferencias en el tipo de cambio: al mostrar los costes en dólares convertidos a RMB se utiliza el tipo de cambio configurado en la plataforma; es normal que difiera del tipo de cambio en tiempo real del banco.
