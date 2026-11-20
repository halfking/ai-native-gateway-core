# Explicación de los datos de la página de gestión de marketing

Esta página gestiona cuatro tipos de herramientas de marketing: paquetes, paquetes de ampliación, códigos de descuento y campañas.

## Las cuatro pestañas

- **Paquetes**: definición del paquete mensual (cuánta cuota de suscripción incluye y cuál es su precio).
- **Paquetes de ampliación**: paquetes de cuota adicional que se compran una sola vez.
- **Códigos de descuento**: códigos que el cliente canjea para obtener un descuento o puntos.
- **Campañas**: estrategias de descuento o regalo por tiempo limitado (borrador → activa → finalizada).

## Reglas de vigencia

- **Campañas de descuento**: cuando una cuenta reclama la campaña, el siguiente pedido recibe automáticamente un descuento proporcional; hasta que se agoten las unidades disponibles.
- **Campañas de regalo**: al reclamarlas, los puntos se conceden y se abonan de inmediato.
- Las campañas en estado «borrador» no afectan a ningún pedido; en estado «activa», se aplican a las cuentas que las hayan reclamado; una vez «finalizadas», no se pueden reclamar de nuevo y los beneficios ya reclamados pueden seguir consumiéndose hasta agotarse según sus respectivas reglas.

## Cómo comprobar las discrepancias cuando las cifras no coinciden

1. El código de descuento figura como «reclamado N veces», pero el pedido no muestra el descuento: el descuento solo se aplica al **siguiente** pedido realizado tras la reclamación; los pedidos anteriores no se ven afectados.
2. Los puntos de regalo no se han abonado: comprueba que el estado de la campaña sea «activa» y no «borrador», y que la cuenta figure en la lista de reclamaciones.
3. El precio del paquete no coincide con el de la página de venta: el paquete tiene un «precio conjunto», distinto del sistema de precio por Token; en los paquetes, la cuota se descuenta según el paquete, sin cobrar Token por Token.
