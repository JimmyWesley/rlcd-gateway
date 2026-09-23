# RLCD Gateway dashboard: design notes

The dashboard should read like a polished developer product: quiet chrome,
loud data, every number explained where it appears. Tokens live in
`src/styles/tokens.css`; this file is the short version.

## Identity

- **Mark** (`LogoMark` in `src/icons/Icon.tsx`): context blocks entering a gate,
  fewer leaving it, one of them reduced to a dot. Indigo-violet gradient
  (`--logo-a` → `--logo-b`), white strokes, 8px corner radius at 32px.
- **Wordmark**: "RLCD" in 800 weight with slight tracking, "Gateway" in 500 weight
  and secondary ink. Never all caps for "Gateway", never a different typeface.
- **Type**: `--font-sans` (Inter when installed, then the system UI sans) and
  `--font-mono` (JetBrains Mono / SF Mono / system mono). No webfonts are shipped,
  so the binary stays self-contained. Mono is for ids, model names, commands and
  numbers in tables; big numbers stay in the sans.

## Color

Light is the base; dark is its own set of steps (not an inversion), applied when
the OS is dark unless the user forced light, or when the user forced dark
(`data-theme` on `<html>`, set by the theme menu, "System" by default).

| Token | Use |
|---|---|
| `--bg`, `--surface`, `--surface-2/3`, `--surface-inset` | page, cards, hovers and wells, inputs and fact boxes |
| `--text`, `--text-2`, `--text-3`, `--text-4` | primary, secondary, muted, disabled ink |
| `--border`, `--border-strong` | hairlines, control outlines |
| `--accent`, `--accent-soft`, `--accent-text` | brand, selection, focus ring (`--accent-ring`) |
| `--good`, `--warn`, `--bad`, `--info` (+ `-soft`, `-text`) | status only, always paired with an icon or a label |
| `--kept`, `--dropped`, `--pending`, `--protected` | pruning decisions (dropped is also hatched) |
| `--saved`, `--shadow` | enforced savings vs shadow-mode "would save" |
| `--cache-read`, `--cache-write`, `--fresh`, `--output` | billed token types |
| `--series-1…8` | categorical series in fixed order (routes, models, clients) |
| `--k-*` | context block kinds in the X-ray |
| `--grid`, `--axis` | chart chrome |

The categorical order is the validated default palette (blue, orange, aqua,
yellow, magenta, green, violet, red), stepped separately for dark. Text never
wears a series color: a swatch or mark beside the label carries identity.
Negative savings are shown as negative, in `--bad-text`, with the reason next to it.

## Space, radius, elevation

- 4px grid: `--s-1` (4) … `--s-10` (40). Cards pad 20, page gutters 24 (16 on small screens).
- Radius: `--r-xs` 4, `--r-sm` 6 (small controls), `--r-md` 8 (inputs, buttons),
  `--r-lg` 12 (cards), `--r-xl` 16 (dialogs), `--r-pill`.
- Shadows are soft and few: `--shadow-sm` on cards, `--shadow-lg` on popovers,
  drawers and dialogs.

## Components

`src/ui/index.tsx` holds the primitives (Button, Card, Badge, Stat, Segmented,
SubNav, Toggle, Field, Callout, EmptyState, CopyField, Drawer, Confirm, Popover,
Disclosure, ModelLabel, RouteLabel). `src/charts` holds the SVG charts
(TimeChart as area, line or stacked columns; Sparkline; Donut; BarList; SplitBar).
Charts follow the same rules everywhere: 2px lines, 4px rounded column tops square
at the baseline, a 2px surface gap between stacked segments, recessive hairline
grids, a legend for two or more series, a hover tooltip, and a hidden data table
for screen readers.

Brand logos (`src/icons/BrandIcon.tsx`) are vendored inline SVGs, used
nominatively next to the name they identify; unknown providers and clients get a
monogram. Sources and licenses: `src/icons/LICENSES.md`.

## Information architecture

Sidebar: Overview · Flow · Traffic, then Control (Savings, Routing), Connect
(Integrations), and Settings at the bottom. Sections with depth use a sub-nav:

- Savings: Results / Settings
- Routing: Routes / Aliases / Rules / Dry run / Sticky conversations
- Integrations: Agents / Recall (MCP) / Apps / Keys
- Settings: Economy model / OpenAI upstreams / General

Everything is a hash route (`#/routing/aliases`, `#/traffic/<id>?route=…`), so
views and filters can be linked and survive a reload. Power-user settings sit
behind sub-views, drawers and "Advanced" disclosures, not on the main screens.

## Language

`src/i18n`: English is the source of truth; `pt-BR` and `es` are typed against it,
so a missing key or a wrong `{param}` fails the build. Numbers, currency (USD),
dates and relative times go through `Intl` for the active locale. Technical
tokens (JSON fields, commands, model ids, headers) are never translated.
