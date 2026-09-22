# web/ — Astro-приложение

Собирает страницы из компонентов [`design/`](../design/) и данных из [`content/`](../content/) + `content/generated/` (результат GitHub sync). Результат сборки — статический HTML, который Nginx раздаёт из `/var/www/krokosha/current`.

**Статус:** дизайн v1 интегрирован (2026-09-22): разметка и стили секций перенесены из `design/pages/*.dc.html` в компоненты Astro, руки героя и пасхалки подключаются прямо из `design/components/`. Как и что перенесено, какие правки сделаны в `design/` — [docs/integration-notes.md](../docs/integration-notes.md). Атрибуты `data-slot` / `data-field` / `data-track` / `data-section` — по [контракту](../docs/contract.md).

## Команды

Нужен Node.js ≥ 22.12 (в CI и на сервере — версия из [`.nvmrc`](.nvmrc)).

```bash
npm ci            # зависимости
npm run dev       # локальная разработка, http://localhost:4321
npm run build     # сборка в dist/
npm run preview   # посмотреть собранное
npm run verify    # всё, что проверяет CI: формат, линт, типы, тесты, mock/, сборка, проверка dist/
npm run test:e2e  # Playwright по собранному сайту (нужен npm run build); локально использует установленный Chrome
npm run mock      # пересобрать ../mock/ после правок content/*.yaml или контракта
```

## Как устроено

```
web/
├── astro.config.ts   адрес сайта (content/site.yaml → url, перекрывается SITE_URL), i18n, CSP, шрифты
│                     (Geologica и Fira Code из пакетов Fontsource — без запросов к Google)
├── src/
│   ├── i18n/         locales.ts — языки и адреса (en → /, uk → /uk/, ru → /ru/);
│   │                 localize.ts — словари { en, uk, ru }, склонения (Intl.PluralRules), плейсхолдеры
│   ├── lib/          schema.ts — строгие схемы content/*.yaml (опечатка в ключе роняет сборку);
│   │                 content.ts — чтение YAML и github.json; data.ts — объекты контракта (profile, skills,
│   │                 projects, site); experience.ts — стаж от career_start; seo.ts — hreflang, JSON-LD;
│   │                 avatar.ts — аватар → AVIF/WebP; pages.ts — тексты из content/pages
│   ├── layouts/      Base.astro — <head> (title/description, canonical, hreflang, Open Graph, JSON-LD, шрифты)
│   │                 и рамка страницы: главная (шапка, левая рамка, панель на телефоне), служебная, /play
│   ├── components/   секции страницы по дизайну; каждая повторяющаяся сущность — один шаблон
│   │                 (ProjectCard, Icon…); lib/icons.ts — иконки, lib/dots.ts — точечные цифры и кольцо
│   ├── scripts/      home.ts — прокрутка (руки, заголовок, рамка, занавес, меню), пасхалки после load;
│   │                 skills.ts — топология навыков; hands.ts — <kro-hands> из design/components/hero
│   ├── pages/        [...lang]/ — index, privacy, play (заглушка), 404 (с игрой), thanks; sitemap.xml.ts; robots.txt.ts
│   ├── styles/       global.css — общие правила дизайна, reduced motion, состояния формы для API
│   └── content.config.ts   коллекция текстовых страниц из ../content/pages
├── public/           favicon, OG-картинка, assets/theme.js (тема до первой отрисовки),
│                     assets/analytics.js — статистика посещений: читаемый, < 3 КБ gzip, без cookie (бриф B5)
├── scripts/          gen-mock.ts — mock/ из content/; check-dist.mjs — проверка собранного сайта;
│                     make-placeholders.mjs — временные фавикон / OG / аватар-заглушка
└── tests/            unit/ — Vitest; e2e/ — Playwright + axe: десктоп и телефон 360 px, обе темы, три языка;
                      любой тест падает при ошибке в консоли или нарушении CSP (fixtures.ts)
```

Правила:

- **Один код — и для сайта, и для мока.** `src/lib/data.ts` строит данные страниц, и он же пишет `mock/<lang>/*.json` (`npm run mock`). CI проверяет, что `mock/` актуален (`npm run mock:check`), поэтому то, с чем работает дизайн, не расходится с тем, что получает сборка.
- **Режим `site` показывает только подтверждённое:** навыки с `confirmed: false` и карточки `TODO(Денис)` на сайт не попадают (в `mock/` они есть — для проверки вёрстки на максимальной плотности).
- **Данные GitHub:** `content/generated/github.json` (пишет sync). Если файла нет (CI, свежий клон) — берётся снимок из `mock/en/projects.json`.
- **Аватар**, первый найденный: `content/avatar.*` (положен вручную) → `content/generated/avatar.*` (скачан sync с GitHub) → нейтральная заглушка `src/assets/avatar-fallback.png`. Конвертация в AVIF/WebP — при сборке.
- **CSP:** Astro сам считает хэши своих скриптов и стилей и кладёт политику в `<meta>`. Поэтому в разметке нет inline-обработчиков и атрибутов `style` (цвет языка репозитория — через SVG `fill`, точки цифр и кольца — через классы); то, что меняется при прокрутке, скрипты пишут в `element.style` — это политика разрешает.
- **Без JavaScript** страница целиком читается и работает: стойки навыков раскрыты (свёрнутый вид — только с `<html class="js">`), «Все репозитории» — `<details>`, меню телефона — `popover`.
- **Импорты с расширением `.ts`:** библиотечный код выполняется в трёх местах — Astro (Vite), Vitest и обычный `node` (скрипты), а встроенная в Node обработка TypeScript требует расширений.
- **Форма заявки** (`ContactForm.astro`, `public/assets/form.js`, страница `thanks`) работает и без JavaScript: обычный POST на `/api/leads`, ответ — страница «спасибо» из релиза, в которую API подставляет номер заявки. Со скриптом — проверка полей на языке страницы, proof-of-work против спама, отправка без перезагрузки. Что в разметке нельзя менять — [docs/contract.md §7](../docs/contract.md); это же проверяет `check-dist.mjs`.
