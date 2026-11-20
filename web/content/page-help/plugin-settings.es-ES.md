# Explicación de los datos de la página de configuración de plugins

Esta página permite elegir qué implementación se utiliza en cada «posición de plugin» de la plataforma (por ejemplo, si la autenticación de inicio de sesión utiliza el componente integrado o un servicio de identidad externo).

## Cómo utilizarla

- Cada posición de plugin se presenta como una tarjeta: la implementación vigente y un menú desplegable con las opciones disponibles.
- **Los cambios solo se aplican después de reiniciar el servicio**: la página guarda la configuración, y el cambio se activa en el siguiente arranque; que el guardado se haya completado correctamente no significa que el cambio ya se haya aplicado.

## Origen de la configuración

- En la parte superior de la página se muestra «Origen de la configuración»: indica si el valor actual procede de la base de datos o de una variable de entorno.
- Los valores procedentes de variables de entorno se sobrescriben con los valores del entorno aunque se modifiquen en la página; para cambiar el origen de forma permanente, debe modificarse el entorno de despliegue.

## Cómo comprobar las discrepancias en los datos

1. Si el comportamiento de inicio de sesión no cambia después de aplicar el cambio: compruebe primero que el servicio se haya reiniciado con la nueva configuración.
2. Si el estado que muestra la página no coincide con el estado real: consulte «Origen de la configuración»; las variables de entorno tienen prioridad sobre los valores guardados en la página.
3. Si una posición de plugin no tiene opciones disponibles: la implementación candidata para esa posición no está desplegada; complete primero la instalación del componente correspondiente.
