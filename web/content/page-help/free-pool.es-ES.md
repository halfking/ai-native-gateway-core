# Explicación de los datos de la página del grupo de recursos gratuitos

Esta página gestiona el «grupo de modelos sin coste»: los modelos de las capas gratuitas oficiales o comunitarias de cada proveedor se agrupan para poder usarlos como respaldo al asignar solicitudes.

## Tres bloques

- **Modelos gratuitos del grupo**: lista de modelos gratuitos realmente disponibles en este momento (un modelo solo se considera «dentro del grupo» si tiene credenciales disponibles).
- **Catálogo de plantillas**: proveedores gratuitos conocidos y sus modelos previstos; «integrado» significa que el grupo tiene credenciales activas de ese proveedor, mientras que un proveedor no integrado solo es «uno que conocemos».
- **Gestión de credenciales**: registro (almacenamiento cifrado) y estado de las credenciales exclusivas del grupo gratuito.

## Prioridad de enrutamiento

- Las rutas de modelos ya registradas en el catálogo oficial tienen **siempre prioridad** sobre el grupo gratuito; este solo actúa como respaldo «cuando no hay canales oficiales disponibles».
- Los modelos del grupo no están sujetos a ningún compromiso de SLA: el proveedor de origen puede limitar el uso de la capa gratuita o dejar de ofrecerla en cualquier momento.

## Cómo comprobar las discrepancias cuando los datos no coinciden

1. La plantilla incluye un modelo que no aparece en la «lista del grupo»: la plantilla es una «lista prevista»; para comprobar la integración real, consulte el estado de las credenciales. El modelo solo entra en el grupo tras añadir las credenciales correspondientes.
2. Las llamadas a un modelo del grupo siguen fallando: las capas gratuitas imponen límites de uso con frecuencia; los fallos se cuentan como errores de credenciales y varios fallos consecutivos provocan que el modelo se retire automáticamente.
3. Un modelo «gratuito» genera registros de coste: es posible que la ruta de respaldo acabe utilizando un canal oficial (porque no se encontró el modelo en el grupo gratuito); la ruta real que aparece en los detalles de la solicitud es la referencia.
