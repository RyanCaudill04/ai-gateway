# Design spec — final ("Current", v3)

The exact values below are taken from `source/*.dc.html`. Where this file and the source
disagree, the source wins.

## 1. Principles

1. **Mostly white, colour only means something.** Blue, green and red each carry a meaning
   (below). Nothing is coloured for decoration, except the robot.
2. **Soft and industrial.** Floating rounded cards with soft shadows on a smooth grey ground.
   No textures, no hairline grids, no gradient washes.
3. **Reactive motion.** Springy hover and press effects, things that rise in, flowing lines that
   show work happening. Every animation is disabled under `prefers-reduced-motion: reduce`.
4. **Light and dark are equal citizens.** Both themes are designed; neither is a derived
   afterthought.

## 2. Colour tokens

| Token | Light | Dark | Use |
| --- | --- | --- | --- |
| `--ground` | `#ECEFF3` | `#05070A` | Page background behind the cards |
| `--card` | `#FFFFFF` | `#10151C` | Floating cards; active pill |
| `--well` | `#F4F6F9` | `#171D26` | Rounded inner panels inside cards, inputs, chips |
| `--well2` | `#E8ECF1` | `#1F2631` | Hover / selected rows, bar tracks |
| `--line` | `#E3E7ED` | `#232B37` | Rarely used; dividers are avoided |
| `--ink` | `#0C1220` | `#EEF2F7` | Primary text |
| `--muted` | `#566174` | `#8B97A8` | Secondary text (passes 4.5:1 on card and well) |
| `--blue` | `#4285F4` | `#8AB4F8` | Fills: primary action, Balanced tier |
| `--blue-ink` | `#1A73E8` | `#8AB4F8` | Blue used as text |
| `--green` | `#34A853` | `#81C995` | Fills: healthy/live, Fast tier, headroom bars |
| `--green-ink` | `#188038` | `#81C995` | Green used as text |
| `--red` | `#EA4335` | `#F28B82` | Fills: attention/errors, Deep tier, quota over 85% |
| `--red-ink` | `#D93025` | `#F28B82` | Red used as text (Revoke, error status) |
| `--on-blue` | `#FFFFFF` | `#0A0E14` | Icon/text on a blue fill |
| `--shadow` | `0 1px 2px rgba(12,18,32,.04), 0 12px 32px -14px rgba(12,18,32,.14)` | `inset 0 1px 0 rgba(255,255,255,.04), 0 12px 32px -14px rgba(0,0,0,.7)` | Card elevation |
| `--stage` | `linear-gradient(180deg,#FBFCFD,#EEF1F5)` | `linear-gradient(180deg,#151B24,#0C1016)` | Robot stage and routing stage |

All the hues are Google's exact brand and Material colours. Light mode uses the brand colours for
fills and the darker Google shades for text. Dark mode uses Google's light dark-theme shades
for both.

### Meaning of each colour

| Colour | Router tier | Other meanings |
| --- | --- | --- |
| Green | **Fast** | Provider up, live, headroom remaining, 200 OK |
| Blue | **Balanced** | Primary action (Send, Create key), focus ring, selection, robot eyes, "listening" |
| Red | **Deep** | Errors (429 → fallback), quota over 85% used, Revoke, robot antenna |

- Tier is always shown as a **small round dot plus a label**, never as a large fill. The one
  exception is the selected tier pill in Routing, which gets a coloured ring and glow.
- **Text on Google brand fills fails contrast**, so never put text on blue, green or red
  fills. Use ink on a tinted or white surface with a coloured ring instead. The Send arrow on
  blue is an icon (3:1 is enough).

### Thinking colours

The "thinking" pixels cycle **blue → red → green → blue** (Google mark order).

## 3. Type

- **Geist** (400/500/600/700) for UI. **Geist Mono** (400/500) for model names, latencies,
  keys, timestamps and endpoint.
- Sizes:

  | Element | Size and weight | Letter spacing |
  | --- | --- | --- |
  | Card titles | 17/600 | −0.01em |
  | Landing headline | 26/600 | −0.035em |
  | User prompts in chat | 16/500 | |
  | Body | 16, line-height 1.65 | |
  | Labels | 13/500 muted | |
  | Mono meta | 12 | |
  | Big numbers | 30–32/600 | −0.03em |

## 4. Layout: app shell with top bar

```
┌─────────────────────────── top bar (card, radius 24) ───────────────────────────┐
│ [robot logo] current      ( New | Chats | Routing | Keys & usage )        (☾)  │
└─────────────────────────────────────────────────────────────────────────────────┘
┌ side list ┐ ┌──────────── main card (8/12) ────────────┐ ┌─ inspector (4/12) ─┐
│ 280px     │ │ header: title + chip/action              │ │ stacked "wells"    │
│ card      │ │ content                                  │ │ (radius 20)        │
│ radius 28 │ │ composer / footer                        │ │                    │
└───────────┘ └──────────────────────────────────────────┘ └────────────────────┘
```

- **Page:** `padding: 12px`, `gap: 12px` everywhere, `height: 100vh` (min 760 px). The ground
  shows between cards.
- **Top bar:**
  - Logo and the word "current" on the left.
  - Nav tabs in the middle, as pills inside a `--well` pill track. The active tab is a white
    `--card` pill with `--shadow`.
  - A round theme button on the right.
- **Side list (280 px):** contents depend on the screen (chats / recent requests / API keys).
  Rows are radius 16; the selected row has a `--well2` background.
- **Body:** a 12-column grid with gap 12. The main card spans 8, the inspector spans 4.
- **Inspector:** a stack of `--well` panels (radius 20, padding 16) with 12 px gaps. Use space
  between sections, not divider lines.
- **Radii:**

  | Element | Radius |
  | --- | --- |
  | Cards | 28 |
  | Top bar | 24 |
  | Stages | 22 |
  | Wells | 20 |
  | Rows | 16 |
  | Composer | 24–26 |
  | Pills, segmented controls, progress bars | 999 |
  | Send button | circle |

- **At ≤960 px:** the side list hides, the body becomes one column and the page scrolls.

## 5. Components

- **Segmented control** (tier picker, chart range, theme): a `--well` pill track. The selected
  option is a `--card` pill with `--shadow`. Each tier option shows its coloured dot; "Auto"
  shows a hollow ring.
- **Composer:**
  - A `--well` pill holding a borderless textarea and a round blue Send button.
  - On focus: a 2 px blue ring plus a 6 px ring of blue at 16% opacity.
  - Send hover: scale 1.08 with a blue glow. Send press: scale 0.92.
- **Chat messages:**
  - The user's prompt is a right-aligned `--well` bubble (radius 22 22 6 22).
  - The model reply is unboxed text, preceded by a mono meta line:
    `● tier · model · provider · latency`.
  - Code blocks are dark (`#0F1520`) with radius 20.
- **Thinking indicator:** a `--well` pill containing four 10 px rounded squares in the thinking
  colours, staggered 0.12 s, pulsing opacity and lifting 3 px. Then "Thinking" and the mono
  tier and model.
- **Status dot:** an 8–9 px circle that pulses while active.
- **Progress bars:** 8–10 px tall on a `--well2` track, fully rounded. They grow in from 0 with
  a staggered 0.1 s delay.

## 6. Motion

- **Springy UI easing:** `cubic-bezier(.2,1.4,.4,1)` (overshoot) for hover and press scales.
- **Smooth easing:** `cubic-bezier(.2,.8,.2,1)` for rises and slides.

| Effect | Spec |
| --- | --- |
| Rise in | translateY 12 px → 0 with fade, 0.8 s |
| Row hover | `--well2` background, translateX 3 px |
| Nav hover | scale 1.04–1.06 |
| Flow lines | dashed gradient background animated along the line (0.5–0.6 s loop); only "live" while work is happening or on the chosen lane |
| Packet | a blue dot travels prompt → classifier on a loop (1.4 s) |
| Chart | line draws via stroke-dashoffset (1.6 s), area fades in after |
| Selection swaps (Routing) | new values rise in over 0.5 s |

- All of it is disabled under `prefers-reduced-motion`.

## 7. The robot (landing page)

An original isometric "voxel" character in the spirit of a chunky retro mascot. It is
generated in code: `robot(mode, lx, ly)` in `source/Main.dc.html`.

- **Projection:** a unit cube at scale s = 13 px.
  `screenX = (x − y)·0.866·s`, `screenY = (x + y)·0.5·s − z·s`.
  SVG `viewBox="-85 -150 200 290"`.
- **Materials:**

  | Part | Material |
  | --- | --- |
  | Shell | white with gradient faces (top `#FFF→#EDF1F5`, front `#F8FAFC→#D6DDE6`, side `#D3DAE3→#A9B3C1`) |
  | Joints | dark gradient |
  | Visor | `#202938→#05070A` with a faint white glare |
  | Chest | a circuit-board panel `#24935F→#13563A` with chip, gold pins `#E0AE4A`, screws, red and green LEDs |
  | Eyes | Google blue with a `drop-shadow` glow |
  | Antenna tip | Google red |
  | Ear light | blue |

- **Parts, back to front:** left arm, legs and feet, body, neck, head, face, fan (5 blades, on
  the head's top plane), antenna, right arm.
- **Intro:** parts drop in staggered (legs 0.2 s, body 0.5 s, arms 0.9 s, head 1.2 s, fan
  1.5 s, antenna 1.7 s) with an overshoot spring.
- **Idle:**
  - It floats ±7 px on a 5 s loop; the ground shadow shrinks and fades in sync.
  - Eyes blink every 5 s.
  - The right arm waves every 7 s.
  - The fan spins slowly; LEDs and the antenna blink.
- **Reactive states** (the `mode` prop):

  | Mode | Trigger | What changes |
  | --- | --- | --- |
  | `idle` | — | Status "Idle" |
  | `typing` | Composer input; reverts after 900 ms of no typing | Head lifts 6 px, eyes squint, fan and antenna speed up; status "Listening" with a blue dot |
  | `thinking` | Send; lasts until the response arrives (4.2 s in the mockup) | Eyes become four cycling blue/red/green/blue pixels, fan spins fast, LEDs flicker; status "Thinking" with a green dot |

- **Cursor follow:** the eyes translate up to ±6 px horizontally and ±4 px vertically toward
  the pointer within the stage.
- **No base:** it floats; there is no circuit board under it (explicitly removed).

## 8. Screens

### Landing (`Main.dc.html`)

- **Side list:** chats ("Today").
- **Main card:**
  - Header "New conversation" with an endpoint chip `ai.ryancaudill.dev/v1`.
  - The robot stage (smooth gradient, status chip top-left, hint text bottom-left).
  - The "What are we working on?" headline, the composer, and the tier picker
    (Auto/Fast/Balanced/Deep) with a one-line hint for the selected tier.
- **Inspector:**
  - Start-with prompts (link to a new chat).
  - Providers: "4 of 4 up", with each provider's green dot and latency.
  - Requests today.

### Chat (`Chat.dc.html`)

- **Side list:** chats, with the current one selected.
- **Main card:**
  - Header: thread title, an "Auto · mostly Balanced" chip, and a Share pill.
  - A scrolling message stream.
  - A docked composer.
- **Inspector:**
  - Status (Thinking/Answered).
  - Routing path: Prompt → Classifier (ink pill) → three tier pills with the chosen one ringed.
    The vertical flow lines animate while thinking.
  - Classifier confidence bars.
  - Served-by grid: model, provider, fallback, key.

### Routing (`Routing.dc.html`)

- **Side list:** live recent requests. Clicking one replays its path.
- **Main card:**
  - The stage: a prompt card, then an animated packet, then a dark Classifier pill with a
    spinning blue/red/green mark.
  - Three tier lanes, each with a flow line, a tier pill (dot, name, score), a score bar and its
    fallback chain (`model → fallback`). The chosen lane is fully opaque, shifted 8 px, its
    flow is live and its pill has a coloured ring and glow; the others sit at 50% opacity.
  - Below the stage: Served by / Provider / Total latency wells.
- **Inspector:**
  - Signals the classifier saw (mono chips).
  - Today by tier: a segmented bar plus percentages.
  - Fallbacks today.

### Keys & usage (`Keys.dc.html`)

- **Side list:** API keys, each with a live green dot and last-used time. A blue "Create key"
  pill sits at the bottom.
- **Main card:**
  - Header: the key name and a "Revoke key" pill in red text.
  - Key row: the masked key, its scope, and Show/Hide and Copy buttons.
  - Requests chart: a big total, a 24h/7d/30d segmented control, and a smooth curve with a blue
    line and a 16% blue area.
  - Recent requests table: rows are rounded white pills; status is green for 200 OK and red
    for "429 → fallback".
- **Inspector:**
  - Free-tier headroom per provider: green bars, turning red with red numbers above 85% used.
  - Rate limit, tiers allowed, created date, errors today (red).

## 9. Accessibility (kept in the mockups; keep it in the build)

- Real `<button>` and `<a>` elements. Icon-only buttons have an `aria-label`. Segmented controls
  use `role="radiogroup"` / `role="radio"` with `aria-checked`. The current nav item and chat
  use `aria-current`. The thinking indicator uses `role="status"`.
- Touch targets are ≥ 40–48 px. Text meets 4.5:1 (that's why the `*-ink` tokens exist).
- Every animation is disabled under `prefers-reduced-motion`.
