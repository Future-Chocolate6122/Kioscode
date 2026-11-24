# Logotipo de Kioscode

La «K modular» es una K hecha con cinco módulos cuadrados, como las celdas de un código.
El módulo central representa el acceso y va en azul de acento.

## Archivos

| Archivo | Uso |
|---|---|
| `kioscode-mark.svg` | Símbolo monocromo (`currentColor`) |
| `kioscode-mark-color.svg` | Símbolo a color |
| `kioscode-mark-white.svg` | Símbolo en negativo, sobre la banda `#00618A` |
| `kioscode-logo.svg` | Símbolo y nombre, monocromo (`currentColor`) |
| `kioscode-logo-color.svg` | Símbolo y nombre a color |
| `kioscode-logo-white.svg` | Símbolo y nombre en negativo, sobre la banda `#00618A` |
| `kioscode-favicon.svg` | Favicon: baldosa azul a sangre que se ve en pestañas claras y oscuras. Base del PNG de 32 px y del apple-touch-icon |
| `kioscode-preview.html` | Vista previa de todas las variantes |

## Construcción

- **Módulo (m):** un tercio de la altura del símbolo.
- **Símbolo:** 3 × 3 m. `kioscode-mark*.svg` usa una retícula de 32 con 4 unidades de margen interno (m = 8), así que queda a píxel exacto a 16, 32, 64… px.
- **Logotipo:** símbolo de 24 unidades, separación de 1 m y el nombre con una altura de mayúscula de ¾ del símbolo. El `viewBox` es `0 0 153 24` y queda ajustado al dibujo, sin margen.
- **Nombre:** Montserrat Bold (700, licencia SIL OFL) convertida a trazados, con su interletrado.

## Zona de respeto

Hay que dejar **1 módulo libre por cada lado**, es decir, ⅓ de la altura del símbolo. Con el logotipo a 24 px de alto son 8 px.

## Tamaño mínimo

- **Símbolo:** 16 px. Por debajo de 24 px conviene usar `kioscode-favicon.svg`.
- **Logotipo:** 16 px de alto.
- **Nitidez:** el símbolo se ve más limpio a múltiplos de 16 px, y el logotipo a alturas múltiplo de 3 (24, 30, 36…).
- **Cabecera de /admin (72 px):** logotipo a **24 px** de alto.

## Colores

| Color | Uso |
|---|---|
| `#00618A` | Asta, brazos y nombre (versión a color); fondo del favicon |
| `#079BE2` | Módulo central (versión a color) |
| `#00ACEC` | Módulo central del favicon |
| `#FFFFFF` | Versión en negativo y K del favicon |

No se usa rojo (`#CD2027`), reservado a los errores. En negativo, todo va en blanco, sin acento.

## Notas técnicas

- `currentColor` solo hereda el color si el SVG va **embebido en línea** en el HTML. Cargado con `<img src>` se ve negro, así que en ese caso hay que usar `-color` o `-white`.
- En el README de GitHub, para que se vea bien en el tema oscuro:

  ```html
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/kioscode-logo-white.svg">
    <img src="docs/kioscode-logo-color.svg" alt="Kioscode" height="48">
  </picture>
  ```

- Para generar los PNG a partir del favicon:

  ```sh
  rsvg-convert -w 32  -h 32  kioscode-favicon.svg -o favicon-32.png
  rsvg-convert -w 180 -h 180 kioscode-favicon.svg -o apple-touch-icon.png
  ```

## Qué no hacer

No separar ni redistribuir los módulos, no girar ni deformar el símbolo, no cambiar los colores y no poner la versión a color sobre la banda azul (en ese caso, va la versión en blanco).
