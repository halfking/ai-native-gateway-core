# Explicación de los datos de la página de precios de mercado

Esta página gestiona los «datos de mercado externos»: recopila los precios de mercado de los modelos desde distintas plataformas para usarlos como referencia comparativa y de fijación de precios. Esta página es de **conciliación de solo lectura + gestión de metadatos** y no establece precios directamente.

## Dos bloques

- **Registro de plataformas**: indica de qué plataformas de mercado se recopilan los datos. Las plataformas marcadas como «Propias» o «Desactivadas» no participan en la sincronización de cotizaciones ni en el cálculo del precio mínimo del mercado.
- **Registro de sincronizaciones**: registro de ejecución de las últimas 10 recogidas (cuándo se realizaron, cuántos datos se obtuvieron y cuántos se rechazaron).

## Identidad de conciliación

Cada sincronización cumple: **registros de origen = registros almacenados + registros rechazados**. Los tres números se pueden consultar en el registro. Para comprobar la causa de las diferencias, abra los detalles de los registros rechazados (motivos habituales: moneda/región no coincidentes o entradas duplicadas).

## Reglas de correspondencia entre regiones y monedas

Las plataformas internacionales ↔ dólar y las plataformas nacionales ↔ renminbi deben configurarse por pares. Las entradas con una combinación incorrecta se rechazarán durante la sincronización.

## Cómo comprobarlo cuando los números no coinciden

1. Si en la columna «Rechazados» del registro aparece un número en rojo: es el mecanismo de conciliación previsto. Abra los detalles de esa sincronización para consultar el motivo concreto del rechazo.
2. Si la cotización no coincide con el precio de mercado que usted observa: los datos de mercado tienen un ciclo de recogida y no se actualizan en tiempo real; al compararlos, confirme el «día de sincronización».
3. Si una plataforma no muestra nunca datos: compruebe en el registro si está desactivada o si el sincronizador todavía no se ha ejecutado para esa plataforma.
