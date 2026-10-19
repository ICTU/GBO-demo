# NL Design System CSS

Copied unchanged from npm, so the pages of the authorization server need no external assets:

| File | Package | Version |
|---|---|---|
| `voorbeeld-theme.css` | `@nl-design-system-unstable/voorbeeld-design-tokens` (`dist/theme.css`) | 14.1.0 |
| `utrecht-components.css` | `@utrecht/component-library-css` (`dist/index.css`) | 9.0.15 |

Both are licensed under EUPL-1.2 (`LICENSE.md`). The Voorbeeld theme is the neutral example theme of NL Design System: it has no logo and no house style of an organisation. Its fonts (Noto Sans, Noto Serif) are not bundled; the browser falls back to its sans-serif and serif fonts.

To update, replace the files with the `dist` files of a newer version and check the pages.
