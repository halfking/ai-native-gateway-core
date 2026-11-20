# Explicación de los datos de la página del informe de conciliación

Esta página es el «conciliador de cuentas»: cada madrugada consolida los movimientos del día anterior en una página de conciliación que puede comprobarse desde distintas perspectivas: proveedor/inquilino/persona/modelo.

## Cómo se generan las instantáneas

- La tarea de agregación diaria genera la instantánea del **día anterior** a la madrugada; los datos del día actual no están disponibles hasta la madrugada siguiente, cuando la página muestra el mensaje «La instantánea de hoy se generará a la madrugada del día siguiente».
- Los días sin instantánea (por ejemplo, si el sistema estaba fuera de servicio) se marcan como «Sin agregar»; la «Fecha de finalización del reproceso» permite completar manualmente el cálculo.

## Dos bases de cálculo de importes (sin combinarlas)

- **Coste del proveedor (USD)**: importe que la plataforma paga al proveedor de origen.
- **Puntos internos / Importe interno**: base de facturación que la plataforma aplica a los inquilinos.
- La diferencia entre ambos es el margen bruto de la plataforma; la página los presenta por separado y no combina importes expresados en unidades distintas.

## Los cuatro componentes de Token

Entrada (volumen de texto enviado al modelo), Salida (volumen de texto generado por el modelo), Lectura de caché (reutiliza la entrada anterior; no se cobra de nuevo o se cobra con descuento), Escritura de caché (entrada que se guarda en caché por primera vez). La suma de los cuatro componentes ＝ el total de Token.

## Qué hacer si las cifras no coinciden

1. La instantánea es la «conciliación final»: es normal que sus cifras no coincidan con las del panel en tiempo real: la instantánea se congela a la madrugada del día siguiente y después no cambia; el panel en tiempo real incluye los datos del día actual.
2. Las fechas marcadas como «Sin agregar» no tienen cifras: complete el reproceso y vuelva a compararlas.
3. Al contrastar la perspectiva del proveedor con la facturación del proveedor, compruebe la zona horaria: este sistema delimita los días según UTC, mientras que la factura del proveedor puede utilizar la zona horaria local; el tráfico que cruza la medianoche se imputará a un día distinto.
4. Las solicitudes fallidas también generan un pequeño coste (por ejemplo, una respuesta parcial que el proveedor de origen ya haya facturado); en los días con una tasa de fallos elevada, el coste por solicitud será relativamente alto.
