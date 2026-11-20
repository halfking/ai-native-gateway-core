# Página de configuración de aprobaciones

Define qué operaciones requieren aprobación manual: condiciones de activación, aprobadores y política de tiempo de espera.

## Criterios de definición

Una política consta de tres elementos: condiciones + personas + tiempo de espera; cambiar la configuración solo afecta a las aprobaciones nuevas que se activen después.

## Cómo comprobar las discrepancias en los datos

- 1.Se configuró, pero no se generó ninguna aprobación: confirmar el ámbito de las condiciones de activación (granularidad por tenant/clave) y el estado de la opción de activación.
- La aprobación o el rechazo automático por tiempo de espera no se aplican: un valor de tiempo de espera de 0 indica una espera indefinida y no cierra el caso automáticamente.
- El aprobador no recibe notificaciones: comprobar si la cuenta del aprobador sigue habilitada.
