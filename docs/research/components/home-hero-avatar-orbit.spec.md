# Home Hero Avatar Orbit Specification

## Overview

- **Target files:** `index.html`, `css/site.css`
- **Reference screenshot:** `docs/design-references/bloome-hero-avatars-1440.png`
- **Interaction model:** time-driven, decorative, non-interactive
- **Source component:** `web-clone/bloome-clone/src/components/Hero.tsx`

## DOM Structure

An `aria-hidden` canvas is positioned behind Miraphant's existing Hero copy. It
contains six empty-alt images in Bloome's original order: portrait-01,
vibe-01, portrait-03, portrait-02, vibe-02 and portrait-04. No Miraphant text,
button or navigation element moves into this layer.

## Exact Visual Rules

- Canvas: 1440px maximum width, 1440/520 aspect ratio, 48px from Hero top.
- Images: circular crop and `drop-shadow(0 14px 30px rgba(37,86,182,.18))`.
- Source files: six original 256×256 RGBA PNG files copied byte-for-byte.
- Sizes: 104, 153 and 115px rhythm on the left; 104, 153 and 104px on the right at a 1440px canvas.
- Positions and widths use the original `Hero.module.css:79-118` values.

## Behavior

- Animation: `7s ease-in-out infinite`.
- Distance: translateY from 0 to -8px and back.
- Delays: 0s, -2.2s, -4.1s, -3.2s, -1.1s and -5s.
- No hover, pointer tracking, click state, scrolling dependency or JavaScript.
- The canvas uses `pointer-events:none`.

## Responsive Behavior

- Desktop above 899px: six avatars render around the unchanged central copy.
- At and below 899px: the complete avatar canvas is hidden, matching Bloome.
- Reduced motion: all avatar animation is removed.

## Explicit Exclusions

Bloome's title, body copy, header, download cluster, typography and mobile blue
gradient are not copied. The task only imports its Hero avatar layer.
