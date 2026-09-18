# krokosha-site

Source of [krokosha.xyz](https://krokosha.xyz), the personal site of **Krokosha**, a network & infrastructure engineer (networks, servers, DevOps, tooling).

> **Status:** phase 0. The repository skeleton, data contract and design brief are in place, and design work runs in parallel. No application code yet.

## Stack

| Layer | Choice |
|---|---|
| Frontend | Astro + TypeScript, static HTML, islands for interactivity |
| Motion | GSAP + ScrollTrigger, Lenis, Canvas/WebGL (hero), Lottie/Rive (characters) |
| API | Go: a single binary under systemd (analytics, admin, leads, Telegram bot, mail) |
| Storage | SQLite (WAL) |
| Edge | Nginx + Let's Encrypt (Certbot) |
| Mail | docker-mailserver (Postfix, Dovecot, Rspamd, OpenDKIM) |

Everything shown on the site (projects, avatar, years of experience) is baked into static HTML at build time. A timer re-syncs GitHub data and rebuilds every 6 hours.

## Layout

```
krokosha-site/
├── design/     Claude Design zone: tokens, components, assets, page previews
├── web/        Astro app that wraps design/ components and injects data
├── content/    Editable content (YAML). Texts live here, not in markup
├── mock/       Sample data in contract format (for design and tests)
├── api/        Go API + CLI
├── deploy/     install / update / backup / rollback, nginx, systemd, mail
└── docs/       Brief, data contract, architecture, design prompt, ops notes
```

Each top-level directory has its own `README.md` describing its sub-structure and rules.

## Key documents

- [docs/brief/MASTER_PROMPT.md](docs/brief/MASTER_PROMPT.md): full project brief (RU)
- [docs/contract.md](docs/contract.md): data contract between design and backend (RU)
- [docs/architecture.md](docs/architecture.md): architecture decisions (RU)
- [docs/design/CLAUDE_DESIGN_PROMPT.md](docs/design/CLAUDE_DESIGN_PROMPT.md): the brief for the design agent (RU)

## Contributing

`main` is protected: every change goes through a pull request. Design changes touch `design/` only (see [design/README.md](design/README.md)).

## License

Code: [MIT](LICENSE). Content, photos, the Krokosha name and character artwork: all rights reserved.
