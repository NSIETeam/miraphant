# Miraphant design provenance

This file is the source lock for the current visual system. New visual or
interaction rules should not be added unless they map to one of the references
below or preserve an existing functional requirement.

## Shared visual primitives

| Miraphant decision | Reference | Adopted rule |
| --- | --- | --- |
| Background, foreground, card, secondary, muted and border colors | `web-clone/xai-bot-clone/src/app/globals.css:51-80` | `#fff`, `#0a0a0a`, `#f8f7f5`, `#f0efed`, `#7d8187`, `#e6e6e4` |
| Page width and gutters | `web-clone/xai-bot-clone/src/app/globals.css:75-80,147` | 1232px maximum; 32px desktop gutter; 20px small-screen adaptation |
| Section heading | `web-clone/xai-bot-clone/src/app/globals.css:148` | 36/40px, medium weight, -0.02em tracking |
| Primary and secondary buttons | `web-clone/xai-bot-clone/src/app/globals.css:149-152` | 44px minimum height, pill radius, black and warm-gray surfaces, color-only hover |
| Hero heading and body | `web-clone/xai-bot-clone/src/components/HeroSection.tsx:150-166` | 60/60px desktop heading; 18/29px body; 42/44px small-screen adaptation |
| Cards and proof surfaces | `web-clone/xai-bot-clone/src/app/globals.css:54,60,67,75` | Warm card background, 1px hairline, 16px base radius; no decorative color |
| Reduced-motion behavior | `web-clone/typeless-clone/src/app/globals.css:239-247` | Remove animation and smooth scrolling when reduced motion is requested |

## Navigation

| Miraphant decision | Reference | Adopted rule |
| --- | --- | --- |
| Navigation copy | Original Miraphant content at `9295d10:index.html` | Preserve `About / Products / Manifesto / AI Club` and `关于我们 / 产品 / 我们的主张 / AI Club`; reference projects influence styling only |
| Header shell | `web-clone/typeless-clone/src/components/SiteHeader.tsx:11-21`; user-directed fixed behavior | Fixed, 88px-high pure-white header with a centered 1280px inner shell |
| Navigation placement | `web-clone/typeless-clone/src/components/SiteHeader.tsx:23-36` | Navigation is absolutely centered and remains independent of the logo and right-side controls |
| Navigation typography and hover | `web-clone/typeless-clone/src/components/SiteHeader.tsx:27-35` | 16px medium links, 8px radius, 16px horizontal padding and `#eee` hover surface |
| Language placement and selector | `GitHub-pre/IMD/frontend/src/app/components/landing/PublicFooter.tsx:253-264`; `LanguageSelector.tsx:25-92` | Removed from the header and placed at the footer's lower-right; 34px pill with globe, full current-language label and chevron; 176px menu opens upward, aligns right and marks the selected locale with a check. Miraphant keeps only English and Chinese |
| Mobile menu icon | `web-clone/typeless-clone/src/components/SiteHeader.tsx:47-52` | Mobile menu icon remains visually unframed |
| Mobile menu lifecycle | `web-clone/minimaxi-clone/src/components/SiteHeader.tsx:344-374` | Escape closes, desktop resize closes, body scroll locks only when open |
| Cross-route header continuity | User-directed functional requirement | All public routes use the same header structure; whole-page fade-out/fade-in is removed; same-origin cross-document navigation keeps the fixed header as a zero-duration named shared element |

## Page composition

| Miraphant area | Reference | Adopted rule |
| --- | --- | --- |
| Home hero hierarchy | `web-clone/xai-bot-clone/src/components/HeroSection.tsx:133-170` | Centered hierarchy, restrained width, copy followed by two actions |
| Home about | `web-clone/minimaxi-clone/src/components/AboutSection.tsx:10-55` | 346px identity column plus wider reading column; the existing two paragraphs remain intact |
| Home collaboration paths | `web-clone/xai-bot-clone/src/components/DownloadGuidesSection.tsx:6-33` | Asymmetric intro plus two stacked action rows; original audience statements become actionable contact paths |
| Collaboration-card hover | `web-clone/minimaxi-clone/src/components/ModelHighlights.tsx:49-55` | 300ms background-color feedback only; image scale and unsourced lift are intentionally omitted |
| Home product cards | `web-clone/xai-bot-clone/src/components/HeroSection.tsx:166-168`; `web-clone/minimaxi-clone/src/components/ModelHighlights.tsx:44-126` | Static surface follows xAI Bot's white, 1px `#E7E7E5`, 24px framed product window; hover/focus follows MiniMax's 300ms `#F7F8FA` product-card response. No lift, shadow, or decorative motion |
| Home AI Club overview | `web-clone/bloome-clone/src/components/Collaboration.tsx:36-63` and `Collaboration.module.css:1-107`; `web-clone/xai-bot-clone/src/components/FeaturesSection.tsx:160-169` | Bloome supplies the three-column white information-card structure on `#f5f5f5`; xAI supplies the same non-interactive `<article>` card's `200ms ease-out` hover translation of `-4px`. Repeated for six existing items, without invented icons, shadows, borders, or color changes |
| Product feature rhythm | `web-clone/typeless-clone/src/components/FeatureRow.tsx:22-40` | Copy and proof areas use stable two-column-to-one-column responsive structure |
| Editorial reading measure | `web-clone/otty-clone/src/app/page.tsx:17-47` | Narrow reading column, left-aligned copy, restrained vertical rhythm |
| Home final content section | `web-clone/minimaxi-clone/src/components/HeroCarousel.tsx:180-214` | Independent white page section with centered copy and 42px-high, 32px-radius actions; it is not grouped with Footer |
| Home footer | `web-clone/minimaxi-clone/src/components/SiteFooter.tsx:97-171` | Footer alone owns the black surface, `#323232` dividers, 16/14px navigation hierarchy and quiet legal row |
| Final CTA on subroutes | `web-clone/xai-bot-clone/src/app/globals.css:51-69` | Black/white inversion only; no additional accent color |
| 404 route | `web-clone/otty-clone/src/app/page.tsx:17-47` and `web-clone/xai-bot-clone/src/app/globals.css:51-80,149-152` | Narrow centered message, monochrome palette and standard primary button |

## Explicit exclusions

- No gradients, colored blobs, pastel product coding, conic patterns or CSS illustrations.
- No floating or morphing navigation shell.
- No card lift outside the sourced xAI AI Club-card response; no arrow rotation, parallax or delayed reveal.
- No visual token outside the sourced monochrome palette.
- Existing product SVG marks and product copy are content, not newly generated decoration.
