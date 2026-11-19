# design/ — UI design for the ai-gateway web app ("Current")

This folder holds the approved UI direction for the gateway's web UI (`ui/`), served at
ai.ryancaudill.dev. It was designed on 2026-11-17 in a design canvas with Claude. It is the
source of truth for how the UI should look and behave when the React app in `ui/` is restyled.

**Start here, in this order:**

1. `screenshots/` — what the final design looks like (light, dark, and key animation states).
2. [`brief-and-decisions.md`](brief-and-decisions.md) — what Ryan asked for, every round of
   feedback, what was rejected and why. Read this before proposing visual changes, so you don't
   reintroduce something he already turned down.
3. [`spec.md`](spec.md) — the final spec: colour tokens, layout, components, motion, the robot,
   and screen-by-screen behaviour. Build from this.
4. `source/` — the original mockup files exactly as they exist on the design canvas (see below).

## Status

| Item | State |
| --- | --- |
| Palette (Google blue / green / red on cool white) | **Approved** — "perfect, no need to change" |
| Layout (top bar + floating rounded cards) | **Approved** |
| Robot mascot (landing page) | **Approved** |
| All four screens | **Approved** ("This all looks amazing") |
| Numbers, model names, latencies in the mockups | **Sample data only** — wire to real router data |
| `[COUNT]`, `[LIMIT]`, `[DATE]`, `[VERSION]`, `[YOUR KEY]` | Placeholders for real values |

## Screenshots

All at 1440×900, 2× pixel density, rendered from the source files with the design tool's own
runtime. Fonts: the design specifies **Geist** / **Geist Mono**, but these renders fell back to
**Inter** / system monospace because Google Fonts was unreachable from the render environment.
Spacing, colour and layout are exact; only the typeface differs.

| File | Shows |
| --- | --- |
| `01-landing-light.png` | Landing / new conversation, idle robot |
| `02-landing-dark.png` | Same, dark theme |
| `03-landing-thinking.png` | Robot in "thinking" state after Send (visor pixels, fan spun up) |
| `04-chat-thinking.png` | Chat while the model is responding (thinking indicator) |
| `05-chat-answered.png` | Chat after the answer arrives |
| `06-chat-dark.png` | Chat, dark theme |
| `07-routing-light.png` | Routing view, a Balanced request selected |
| `08-routing-deep-selected.png` | Routing view, a Deep request selected |
| `09-routing-dark.png` | Routing, dark theme |
| `10-keys-light.png` | Keys & usage |
| `11-keys-dark.png` | Keys & usage, dark theme |
| `12-palette.png` | Palette reference board |
| `13-landing-phone.png` | Landing at phone width (390 px) — layout stacks |
| `history-wireframe-*.png` | The three layout options that were compared (A was chosen, then given a top bar) |

## Source files (`source/`)

Exact copies of the canvas files — not converted. Each `*.dc.html` is a "Design Component":
HTML markup with `{{holes}}`, `<sc-for>` / `<sc-if>` blocks and a `class Component extends
DCLogic` script. They need the design tool's runtime (`./support.js`, not included) to render,
so **don't try to open them directly**. Treat them as precise reference for markup structure,
inline CSS values, keyframes and interaction logic when porting to React.

| File | Screen |
| --- | --- |
| `Main.dc.html` | Landing / new conversation (contains the robot generator) |
| `Chat.dc.html` | Chat |
| `Routing.dc.html` | Routing |
| `Keys.dc.html` | Keys & usage |
| `Palette.dc.html` | Palette board |
| `canvas.json` | Canvas index (artboard layout) |
| `wire/*.dc.html` | Layout wireframes from the exploration |

The live, editable canvas: https://claude.ai/code/artifact/cb6d8b74-89ff-4563-9602-892f4b7fca97
(private to Ryan's account).

## Notes for implementers

- `ui/` is React 19 + Vite + TypeScript. Port the design as real components. Put the colour
  tokens in one CSS file as custom properties, with `.light` / `.dark` (or
  `[data-theme]`) on the root. Don't copy the `.dc.html` runtime.
- The robot is drawn procedurally in code, not as an image. `robot()` in
  `source/Main.dc.html` builds isometric polygons. Port that function as-is (it's plain JS)
  into a `<Robot mode lx ly />` component.
- Colour must stay **functional**: it marks tier, status and action, and never decorates. See
  the rules in `spec.md`.
