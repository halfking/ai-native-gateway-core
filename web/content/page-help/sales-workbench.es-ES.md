# Explicación de los datos de la página del espacio de trabajo de presupuestos

Esta página es el espacio de trabajo interno para presupuestar a los clientes: primero se registra «qué podemos vender» y después «qué quiere el cliente»; el sistema combina ambos datos para generar el presupuesto.

## Cuatro pestañas

- **Pozo de oferta**: lista de modelos que la plataforma puede vender actualmente (incluye el precio de coste, la divisa y el nivel de confianza; todos los datos los genera el servidor y no se pueden editar manualmente).
- **Pozo de demanda**: modelos y consumo que desea el cliente.
- **Presupuesto**: presupuesto formal generado tras cotejar la oferta y la demanda (tiene número de versión y se puede aprobar y compartir).
- **Panel de control** (solo para superadministradores): resumen del embudo de presupuestos y de la desviación de costes.

## Criterios clave

- **Referencia de coste**: el coste de cada línea del presupuesto procede de la tabla de precios de coste de la página de gestión de precios; el coste del presupuesto es una instantánea del momento de su generación y las variaciones posteriores no se aplican retroactivamente a los presupuestos ya emitidos.
- **Versión**: cada revisión del mismo presupuesto genera una versión nueva; las versiones anteriores son de solo lectura y sirven para trazabilidad.
- **Indicador de desviación de costes**: muestra una alerta cuando la diferencia entre el precio de coste utilizado en el presupuesto y el precio de coste actual supera el umbral, para evitar presupuestar con costes obsoletos.

## Cómo comprobar las discrepancias entre los datos

1. Si un modelo no aparece en el pozo de oferta, comprueba primero que su precio de coste esté registrado en la gestión de precios; los modelos sin precio de coste no se pueden vender.
2. Si el coste del presupuesto difiere del precio de coste actual, ten en cuenta que el presupuesto conserva el coste del momento de su generación; consulta el indicador «desviación de costes» para determinar si es necesario rehacer el presupuesto.
3. Si desaparece una entrada del pozo de demanda, significa que la demanda se ha convertido en un presupuesto, se ha archivado en el pozo de demanda y puede verse en la página de presupuestos.
