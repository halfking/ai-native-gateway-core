# Explicación de los datos de la página de usuarios

Esta página gestiona las cuentas de las personas que pueden iniciar sesión y utilizar la plataforma.

## Campos de cada fila

Nombre de la cuenta, rol, estado (activado/desactivado), tenant al que pertenece y hora de la actividad más reciente.

## Roles y visibilidad

- El rol determina qué páginas puede ver una persona después de iniciar sesión: el administrador ve todas y el administrador del tenant solo ve su propio tenant.
- Una cuenta «desactivada» no puede iniciar sesión, pero su consumo histórico y sus registros de solicitudes se conservan y se pueden consultar.

## Criterio de «actividad reciente»

- Se toma la hora más reciente entre el **último inicio de sesión o la última llamada** de esa persona.
- Que aparezca «hace mucho tiempo» no significa que la cuenta tenga alguna anomalía: puede que simplemente no se haya utilizado durante ese periodo.

## Cómo comprobar las discrepancias numéricas

1. El número de usuarios no coincide con el número de asientos configurados en el tenant: los asientos son un concepto de autorización, mientras que la lista muestra las cuentas reales; las cuentas sobrantes pueden proceder de periodos de prueba o de residuos históricos.
2. Hay dos cuentas con el mismo nombre: las cuentas se distinguen por un ID único, por lo que tener el mismo nombre no afecta a la facturación ni a las estadísticas; cambiar el nombre no combina los datos históricos.
3. Una cuenta desactivada hace poco todavía aparece como activa: el estado se aplica de inmediato, pero «actividad reciente» conserva el momento anterior a la desactivación, lo que es normal.
