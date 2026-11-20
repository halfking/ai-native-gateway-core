# Página de cascada de programación

Representa el proceso de programación de cada solicitud como una cascada: los candidatos se prueban uno a uno y se distingue de un vistazo quién acepta la solicitud y quién falla.

## Criterios de cálculo

La longitud de la barra horizontal = el tiempo que tardó ese candidato; las etiquetas de éxito o error aparecen al final de la barra; cada solicitud tiene un único registro final de éxito.

## Cómo comprobarlo cuando no coincidan las cifras

- 1. El número de barras de la cascada es mayor que el número de candidatos: se incluyen los reintentos internos; usa la fila «Éxito final» como referencia.
- El tiempo total difiere de los detalles de la solicitud: la cascada solo incluye la fase de programación, no el tiempo de generación del modelo.
