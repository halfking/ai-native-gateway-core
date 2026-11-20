# Explicación de los datos de la página de análisis de relación con la calidad

Esta página responde a «¿Qué factores influyen en la calidad de las respuestas?»: compara estadísticamente las características de las solicitudes (longitud del prompt, presencia de imágenes, presencia de código y número de llamadas a herramientas) con la puntuación de calidad de las respuestas.

## Bloques principales

- **Predictores de la calidad** (tabla de coeficientes de correlación): intensidad de la relación entre cada característica y la calidad (cuanto mayor sea |r|, mayor será su poder predictivo).
- **Comparación por grupos**: clasifica las solicitudes en grupos según el valor de cada característica (por ejemplo, cinco grupos según la longitud del prompt) y muestra, en cada grupo, la calidad media, la tasa de éxito y la latencia.

## Criterios de cálculo

- La puntuación de calidad procede de la evaluación automática de las respuestas por parte del sistema (una síntesis multidimensional), no de las valoraciones de los usuarios.
- El coeficiente de correlación (Pearson r)取值 -1 ~ +1: valores positivos＝indican que cuanto mayor es la característica, mayor es la calidad; valores negativos＝indican lo contrario; valores cercanos a 0＝indican que no hay relación.
- Los grupos solo incluyen solicitudes con puntuación de calidad; las solicitudes no cubiertas por la evaluación no se incluyen en el denominador.

## Cómo comprobar las discrepancias numéricas

1. La correlación es fuerte, pero no tiene sentido desde el punto de vista empresarial: puede deberse a una causa común (por ejemplo, «las solicitudes con imágenes pertenecen por casualidad a un mismo proveedor»); revise los detalles por grupos.
2. El total de solicitudes de los grupos es < que el total de solicitudes de la página: las solicitudes no evaluadas no se incluyen en los grupos y la diferencia corresponde a la parte «no evaluada».
3. El valor de r cambia al consultar dos veces la misma característica: la ventana temporal es diferente; como r es sensible a la ventana, manténgala fija al comparar.
