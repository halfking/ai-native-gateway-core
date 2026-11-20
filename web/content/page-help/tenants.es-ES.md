# Explicación de los datos de la página de listado de tenants

Los tenants son las «unidades de cliente» de la plataforma: cada tenant tiene sus propias claves, usuarios, cuota y consumo, y estos elementos están aislados entre sí.

## Campos de cada fila

Nombre del tenant, código del tenant (código abreviado único dentro del sistema), estado y uso de la cuota.

## Sistema de cuotas

- **Cuota de suscripción**: crédito de consumo prioritario incluido en el paquete mensual; se reinicia cada mes.
- **Saldo del monedero/efectivo**: fuente de cobro por consumo una vez agotada la suscripción.
- Orden de consumo: primero la cuota de suscripción y después el monedero. En el detalle de la página se muestran por separado; no se combinan en un único «saldo».

## Significado del aislamiento por tenant

- Las claves del tenant A no pueden acceder a la configuración del tenant B; las estadísticas, los registros y las cuotas se contabilizan por separado para cada tenant.
- El «tenant predeterminado» es el espacio operativo interno de la plataforma y se gestiona en paralelo con los tenants de clientes de pago.

## Cómo comprobar las discrepancias numéricas

1. La cuota «restante» no coincide: comprueba primero el momento de la estadística: la cuota se descuenta en tiempo real con cada llamada y las cifras de la página tienen una caché de segundos.
2. El consumo en los detalles del tenant no coincide con el panel general: el panel incluye el consumo interno del tenant predeterminado; para la facturación de un tenant, los detalles del tenant son la fuente de referencia.
3. Un tenant recién creado muestra datos «de ayer» de inmediato: es imposible; si se ven, lo más probable es que el código del tenant esté reutilizando un código histórico; comprueba primero que el código sea único.
