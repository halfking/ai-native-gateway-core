# Página del registro de conexiones

Registre la «identidad de conexión» de cada credencial de origen: nodo, cronología de recuperación e historial de cambios de estado.

## Criterios de referencia

Una credencial por fila; la cronología de recuperación registra cada paso desde la avería hasta la recuperación de esa credencial.

## Cómo comprobarlo cuando las cifras no coinciden

- 1.La credencial figura como normal en la página de supervisión de credenciales, pero como anómala aquí: ambas páginas tienen una perspectiva distinta (la supervisión muestra el estado de funcionamiento en tiempo real y el registro, el archivo histórico); use la cronología como referencia para el rastreo.
- La cronología de recuperación presenta huecos: no se completa retrospectivamente el registro durante los intervalos de reinicio del servicio de recopilación; los huecos son un patrón conocido.
