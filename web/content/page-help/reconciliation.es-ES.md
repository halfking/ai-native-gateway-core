# Explicación de los datos de la página del informe de conciliación

Esta página resume el uso registrado por la pasarela durante el día anterior desde las perspectivas de proveedor, inquilino, persona, modelo y otras. Sirve para conciliar los registros de la pasarela; no es una factura del proveedor ni indica un importe confirmado como pagadero.

## Cómo se generan las instantáneas

- La tarea de agregación diaria genera la instantánea del **día anterior** a la madrugada; los datos del día actual no están disponibles hasta la madrugada siguiente, cuando la página muestra el mensaje «La instantánea de hoy se generará a la madrugada del día siguiente».
- Los días sin instantánea (por ejemplo, si el sistema estaba fuera de servicio) se marcan como «Sin agregar»; la «Fecha de finalización del reproceso» permite recalcular los datos que faltan. También se pueden recalcular las instantáneas existentes; no son definitivas de forma permanente.

## Dos bases de cálculo de importes (sin combinarlas)

- **Importe desde la perspectiva del proveedor**: suma del uso terminal registrado por la pasarela, incluido el uso sin proveedor asignado y el tráfico no comercial. Es una estimación registrada por la pasarela, no un importe devuelto por el proveedor ni un importe confirmado como pagadero o pagado.
- **Créditos internos / importe interno**: solo incluye tráfico comercial; los créditos se convierten usando el factor de precio guardado con la instantánea. Es la base de facturación interna de la plataforma, no una prueba de facturación o cobro.
- Solo conviene comparar ambos importes cuando coinciden el alcance del tráfico y la moneda. La diferencia no equivale automáticamente al margen bruto. La página los presenta por separado y no combina importes expresados en unidades distintas.

## Los cuatro componentes de Token

Actualmente, este informe suma la entrada, la salida, la lectura y la escritura de caché. Los valores de caché pueden solaparse con la entrada. Para comparar informes, usa la entrada normalizada más la salida y muestra la lectura y escritura de caché por separado.

## Qué hacer si las cifras no coinciden

1. La instantánea es un agregado calculado con los datos disponibles en ese momento. Es normal que no coincida con el panel en tiempo real: este puede incluir datos del día actual y la instantánea se puede actualizar al volver a ejecutar la agregación.
2. Las fechas marcadas como «Sin agregar» no tienen cifras: complete el reproceso y vuelva a compararlas.
3. Al comparar la perspectiva del proveedor con una factura, tenga en cuenta tanto el alcance como la zona horaria: esta página suma el uso terminal registrado por la pasarela, incluido el tráfico no asignado y no comercial, y muestra una estimación, no el importe facturado por el proveedor. La factura también puede usar días locales en vez de UTC.
4. Las solicitudes fallidas con uso terminal o coste registrado se incluyen en el agregado de la pasarela; esto no significa que el proveedor haya confirmado un cargo.
