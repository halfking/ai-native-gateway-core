# Explicación de los datos de la página de análisis de correlaciones

Esta página responde a «¿qué factores se relacionan con los fallos?»: compara mediante tablas cruzadas various características de las solicitudes (modelo, longitud y proveedor) con los resultados de fallo para identificar las combinaciones donde se concentran los fallos.

## Cómo interpretar el gráfico

- Cada celda de la交叉 muestra el número de solicitudes y la tasa de fallos de esa combinación; cuanto más oscuro es el color, más concentrados están los fallos.
- «Correlación» no significa «causalidad»: la página muestra asociaciones estadísticas, por lo que que A y B aparezcan juntos no implica que A cause B.

## Criterios de cálculo

- Tasa de fallos＝número de fallos de la combinación ÷ número total de solicitudes de la combinación; las combinaciones con muestras demasiado pequeñas (por debajo del umbral) se señalizan para evitar conclusiones engañosas.
- La ventana temporal y los filtros determinan el alcance de las estadísticas; antes de comparar dos grupos de cifras, comprueba que la ventana sea la misma.

## Cómo comprobar las discrepancias numéricas

1. Una celda tiene una tasa de fallos del 100 %, pero solo contiene 2 solicitudes: es una fluctuación de muestra pequeña y, siguiendo el aviso de la página, no debe interpretarse como estadísticamente significativa.
2. El número de errores difiere del de la página de supervisión de credenciales: esta página agrega por «combinación de características de la solicitud», mientras que la página de credenciales lo hace por «credencial»; se utilizan dimensiones de segmentación diferentes.
3. Una celda queda vacía después de aplicar un filtro: la combinación no tiene muestras para ese filtro, lo que significa «sin datos» y no «cero fallos».
