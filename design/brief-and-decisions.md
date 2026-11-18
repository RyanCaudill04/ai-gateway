# Brief and decisions

How the design got where it is. Ryan's words are paraphrased closely; quotes are his own.

## The original ask

> "I want a unique yet approachable design for my ai gateway application. Currently, it is
> basically like every other ai chat ui. I want it to be more fluid and animated."

- **Reference feel:** his portfolio site (ryancaudill.dev): Apple-app style, *animated but
  minimalist*.
- **Screens wanted:** landing / empty state, chat, model routing view, API keys & usage.

## Round 1: first direction (rejected in parts)

Built: a light UI with an indigo/violet/mint accent mix, a floating pill nav, a breathing
gradient "orb" on the landing page, a bubble-less chat with an animated "route trace" under
each reply, a lane diagram for routing, and a keys/usage dashboard.

**Ryan's feedback:**

- ✅ Animation "feels good, feels reactive."
- ✅ The routing page concept is good. *(Kept through every round.)*
- ✅ The top nav bar was "a step in the right direction."
- ❌ Spacing and use of the screen were sub-par: lots of odd white space.
- ❌ Boxes were oddly and inconsistently sized and shaped.
- ❌ The colour scheme "feels very vibe-coded."
- ➕ Replace the orb with a **complex animation of a robot**: "a more fleshed out, 64-bit,
  Claude-Code-style character with depth. Maybe Raspberry Pi related?"
  - It had to be an **original** character in that chunky, retro-with-depth spirit, not
    Claude Code's actual mascot.

## Round 2: questions and layout options

Answers Ryan gave:

- **Theme:** design light and dark **equally**.
- **Robot concept:** **combine** two ideas: it assembles itself as an intro, then idles and
  reacts (eyes follow the cursor, perks up while typing, spins up while thinking).
- **Layout:** he asked to see wireframes (boxes and lines only) of three structures first:
  - A. full app shell
  - B. top bar + full width
  - C. bento grid

  (See `screenshots/history-wireframe-*.png`.)

**Palette conversation:**

- **Inspiration:** the Google Cloud (GCP) logo, "heavy white concentration with a mixed colour
  highlight." Also his notebook: a loud reddish-orange cover against bright white pages.
- **Temperature:** cool and clinical.
- **Role of colour:** functional. "It should highlight, not be the main attraction."
- **The robot:** should be one of the only sources of colour, and should have a **thinking
  animation** while the model responds.

## Round 3: bento + orange signal (both rejected)

Built: the bento-grid layout, a white UI with one red-orange signal colour (`#FF4F1F`), and the
first robot (blocky isometric, standing on a Raspberry Pi board).

**Ryan's feedback:**

- ❌ "Orange does not fit." He wants **blues, greens and light reds**.
- ❌ "The bento box does not look good." He chose **option A, full app shell** instead.
- Design **all four screens** in that layout.

## Round 4: palette iterations (mid-build feedback)

- First pass used custom muted blue / green / soft red. Ryan: "use deeper colours… more rich,
  not as washed out."
- Then: "Maybe use the exact Google palette." → **`#4285F4` blue, `#34A853` green, `#EA4335` red**
  (Google's brand colours), with Google's darker shades for text and its light shades for
  dark mode.
- Result: **"New colour usage and palette is perfect, no need to change that."**

## Round 5: final polish

Ryan's notes:

- **Robot:** "higher definition," and remove the Pi board it was standing on.
  → It now has gradient-shaded surfaces, articulated arms (elbows and hands), legs and feet, a
  detailed circuit-board chest (chip, gold pins, screws, status LEDs), a visor glare, head and
  side vents, a lit ear, a 5-blade fan, and glowing eyes. It floats over a soft shadow that
  breathes with it.
- **Layout:** "more bubbly, less rigid and separated," and get rid of the textured (dotted)
  background: "smooth, industrial, like Apple products."
  → Hard 1 px dividers and edge-to-edge panels became floating rounded cards (28 px radius,
  soft shadows) on a smooth grey ground, with inner rounded "wells" and pill-shaped controls.
  Smooth gradients replaced the dot texture.
- **Navigation:** the left icon rail became a **constant top bar**, with labelled pill tabs.
- Result: **"This all looks amazing."**

## Things Ryan explicitly rejected (don't bring back)

- Indigo/violet/mint "vibe-coded" accent mixes; gradient-wash decoration.
- Orange or red-orange as an accent.
- Washed-out, desaturated pastels for the functional colours.
- Bento-grid layout.
- Dotted or textured backgrounds.
- Rigid, edge-to-edge panels separated by hairlines.
- A vertical icon rail for primary navigation.
- The robot standing on a circuit-board base.

## Things he liked from the start (keep)

- Reactive, springy motion everywhere.
- The routing visualisation: prompt → classifier → tier lanes with animated flow.
- A top navigation bar.
- Mostly-white surfaces with colour used only to mean something.
