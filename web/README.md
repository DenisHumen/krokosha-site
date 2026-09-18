# web/ — Astro-приложение

Собирает страницы из компонентов [`design/`](../design/) и данных из [`content/`](../content/) + `content/generated/` (результат GitHub sync). Результат сборки — статический HTML, который Nginx раздаёт из `/var/www/krokosha/current`.

**Статус (фаза 1):** каркас с временной разметкой и полным SEO. Вёрстка и стили временные — их заменят компоненты из `design/` (фаза 2). Атрибуты `data-slot` / `data-field` / `data-track` / `data-section` уже стоят по [контракту](../docs/contract.md).

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
├── astro.config.ts   адрес сайта (content/site.yaml → url, перекрывается SITE_URL), i18n, CSP
├── src/
│   ├── i18n/         locales.ts — языки и адреса (en → /, uk → /uk/, ru → /ru/);
│   │                 localize.ts — словари { en, uk, ru }, склонения (Intl.PluralRules), плейсхолдеры
│   ├── lib/          schema.ts — строгие схемы content/*.yaml (опечатка в ключе роняет сборку);
│   │                 content.ts — чтение YAML и github.json; data.ts — объекты контракта (profile, skills,
│   │                 projects, site); experience.ts — стаж от career_start; seo.ts — hreflang, JSON-LD;
│   │                 avatar.ts — аватар → AVIF/WebP; pages.ts — тексты из content/pages
│   ├── layouts/      Base.astro — <head>: title/description, canonical, hreflang, Open Graph, JSON-LD
│   ├── components/   секции страницы; каждая повторяющаяся сущность — один шаблон (ProjectCard, SocialLinks…)
│   ├── pages/        [...lang]/ — index, privacy, play (заглушка), 404; sitemap.xml.ts; robots.txt.ts
│   ├── styles/       global.css — временные стили каркаса
│   └── content.config.ts   коллекция текстовых страниц из ../content/pages
├── public/           favicon, OG-картинка, assets/theme.js (тема до первой отрисовки)
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
- **CSP:** Astro сам считает хэши своих скриптов и стилей и кладёт политику в `<meta>`. Поэтому в разметке нет inline-обработчиков и атрибутов `style` (цвет языка репозитория — через SVG `fill`).
- **Импорты с расширением `.ts`:** библиотечный код выполняется в трёх местах — Astro (Vite), Vitest и обычный `node` (скрипты), а встроенная в Node обработка TypeScript требует расширений.
- **Форма заявки** появится вместе с API заявок: форма, которая шлёт данные в несуществующий адрес, теряла бы обращения.
