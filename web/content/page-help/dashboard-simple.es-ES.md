# Explicación de los datos de la página de resumen del modo sencillo

Esta página ofrece una vista «de un vistazo»: estado general + consumo + acciones rápidas, y está pensada para quienes no necesitan consultar el panel continuamente.

## La página se divide en tres bloques

- **Tarjetas de estadísticas**: cifras clave como el número de solicitudes de hoy, la tasa de éxito y el coste.
- **Estado del sistema**: indica directamente, mediante colores y texto, si los componentes clave funcionan correctamente.
- **Tendencia de los últimos 7 días**: gráfico de barras diario que muestra la evolución del número de solicitudes.

## Criterios de cálculo

- Las tarjetas de estadísticas usan el **criterio de hoy** (desde las 00:00 de hoy hasta ahora; el día cambia según la hora universal UTC).
- El gráfico de tendencias y el panel para expertos utilizan la **misma fuente de datos**: las cifras de ambos modos no se contradicen.
- El gráfico de tendencias solo muestra «días con datos»: es normal que el día 1 de una instalación nueva tenga solo 1 barra; no muestra ceros que no corresponden a datos reales.

## Cómo comprobar las discrepancias entre cifras

1. «Solicitudes de hoy» cambia de día a medianoche UTC: antes de las 8:00, hora de Pekín, el «hoy» del gráfico todavía corresponde al último intervalo de ayer.
2. El denominador de la tasa de éxito es el total de solicitudes, incluidas las bloqueadas: cuando no coincida con «operaciones correctas ÷ total que se supone», compruebe primero si hay solicitudes bloqueadas.
3. El coste se muestra con un solo criterio: el coste interno (en dólares) y los créditos externos no se presentan juntos; deben comprobarse por separado.
4. Si un elemento del estado del sistema indica «Normal», pero una página no se abre: la comprobación verifica que el componente siga operativo, no que cada función concreta esté disponible; para un problema específico, consulte las instrucciones de la página correspondiente.
