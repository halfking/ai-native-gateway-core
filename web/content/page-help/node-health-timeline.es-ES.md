# Página de cronología de salud de nodos

Cronología del historial de salud de una credencial: cuándo está bien, cuándo está mal y cuándo se recupera.

## Explicación de los criterios

Eje horizontal = tiempo; bloques de color = estado; la cronología solo se añade, no se modifica, y es el registro original para rastrear la calidad de las credenciales.

## Cómo comprobar las discrepancias entre los números

- 1.Hay huecos en la cronología: las interrupciones de采集 no se registran retroactivamente (ventana de reinicio del servicio); un hueco ≠ estado normal.
- Si no coincide con el estado de la página de supervisión: la referencia es el bloque de color más reciente de la cronología, ya que la página de supervisión puede tener datos en caché.
