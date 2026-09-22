# Handoff — krokosha.xyz design v1

Structure follows `design/README.md` in the repo. What is in this package:

```
design/
├── tokens.css                      all tokens, light + dark ([data-theme] + prefers-color-scheme)
├── components/
│   ├── hero/hands.js               <kro-hands> — glyph hands (Canvas 2D). attrs: progress 0..1, layout h|v, density
│   └── eggs/eggs.js                ES module: initEggs({flags, texts, experience}), rebootAvatar(el), openTerminal(), nightMode()
├── admin/admin.css                 drop-in for api/internal/admin/static/admin.css (same classes)
└── handoff/CHANGELOG.md            this file
```

Page previews (`*.dc.html` in the project root) are the visual source of truth: every section, its inline
styles, `data-slot` / `data-field` / `data-track` / `data-section` attributes, and the form markup (contract §7:
names, ids, message blocks) are there. Mock data is read from `mock/<lang>/*.json` exactly as the build does.

## Animation spec

| Piece | Trigger | Timing |
|---|---|---|
| Hands fly-in | first paint, after LCP text | 1.6 s total, per-particle delay 0–0.95 s, cubic-out, glyph decode every 60 ms |
| Hands rest | idle | breathing ±1.2 px (sin, 1.3 rad/s); cursor repulsion r = 80 px, spring 0.82 |
| Hands approach | scroll, scrubbed | hero = 260vh, progress = scrollY / (heroH − vh); contact at progress 0.72 |
| Touch | progress ≥ 0.72 | accent flash 90 px → ripple ring (accent glyphs, width 70 px) → 7 bezier lines fan down, packets run at 0.6 Hz; caption `link_caption` fades in 320 ms; events `kro:link` / `kro:unlink` |
| Headline exit | progress 0 → 0.55 | translateY −80 px, opacity 1 → 0 |
| Services numbers | native `position: sticky; top: 3.75rem` per block, min-height 62vh (desktop) | the next block pushes the previous number out |
| Skills tree | click category | rows 0fr → 1fr 640 ms ease-out; rack units slide −14 px → 0 with 70 ms stagger; SVG connectors dash-in 640 ms; packets `offset-path`, 1.2–1.8 s loop |
| Stats → curtain | stats `sticky; top:0`, curtain `z-index:5` scrolls over it | heading parallax −60 px, text −30 px, dot ring +120 px & 20° across one viewport |
| Reduced motion | `prefers-reduced-motion` | hands static (progress only), no repulsion, eggs never auto-start, cat disabled |

## Easter eggs (flags `site.flags.easter_eggs.*`)
console_message → `krokosha.hello()` · konami → night mode (rack LEDs) · sudo_terminal (type `sudo` outside inputs) ·
avatar_reboot (5 clicks on the hero avatar) · croc_footer (peeks at the footer, 5 clicks = extra) · cat_walker (featured cards) ·
lost_packet_404 (port game on 404) · achievements (`localStorage['krokosha:eggs']`, 7 total, toast + event `kro:egg`).

## Not done / notes for integration
- No-WebGL/no-canvas fallback SVG for the hands is not drawn yet (component renders nothing without canvas).
- `form.js` is not loaded in previews; the preview simulates states via the Tweaks panel. In production load `/assets/form.js` as is.
- Fonts: Geologica + Fira Code (Google Fonts, OFL, Cyrillic incl. ґ є і ї). Self-host via Fontsource for CSP.
