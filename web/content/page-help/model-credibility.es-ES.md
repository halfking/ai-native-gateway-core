# Explicación de los datos de la página de confianza del modelo

Esta página comprueba «si el proveedor está utilizando un modelo de gama inferior para hacerse pasar por uno de gama superior» — lo que suele denominarse dilución o pérdida de capacidad. Las cuatro pruebas se aplican de forma cruzada; cuando no permiten obtener una conclusión, se indica honestamente «evidencia insuficiente», sin asumir que no haya problemas.

## Cuatro conclusiones

| Conclusión | Significado |
| --- | --- |
| Verificado | Varias pruebas coinciden y el modelo es auténtico y coincide con lo anunciado |
| En duda | Hay señales sospechosas, pero no son suficientes para determinarlo; se recomienda revisarlo |
| Dilución | Hay pruebas sólidas de que la entrega real es inferior a la configuración anunciada |
| Evidencia insuficiente | No hay suficientes pruebas para determinarlo (**no significa que no haya problemas**) |

## Cuatro pruebas (criterios de evaluación)

La identidad declarada por el proveedor, la comparación de capacidades del mismo modelo, la comprobación de si la firma de las solicitudes ha cambiado y la calibración con una línea base externa.

## Criterios de cálculo

- Los «nodos evaluados» se cuentan por combinación de «modelo × proveedor»; el mismo modelo se evalúa por separado con cada proveedor.
- La conclusión se basa en la **última detección**; la «detección completa» vuelve a ejecutar las pruebas y sobrescribe las conclusiones anteriores.

## Cómo comprobar las discrepancias en los números

1. Hay muchos resultados «En duda», pero no hay anomalías en el negocio: la clasificación «En duda» no es una acusación, sino una alerta para revisar; consúltense también los cambios en las huellas digitales registrados en la página de supervisión de integridad.
2. El resultado cambia a «Evidencia insuficiente» nada más terminar la detección: esa combinación ha tenido poco tráfico últimamente y la muestra es insuficiente; vuelva a realizar la detección cuando haya tráfico real.
3. No coincide con las capacidades que figuran en el panel del proveedor: la «comparación de capacidades» de las cuatro pruebas depende del tráfico medido en esta plataforma, por lo que los modelos de uso poco habitual tienen, de forma natural, menos evidencia.
