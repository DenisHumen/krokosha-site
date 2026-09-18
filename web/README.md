# web/ — Astro-приложение

Собирает страницы из компонентов [`design/`](../design/) и данных из [`content/`](../content/) + `content/generated/` (результат GitHub sync). Результат сборки — статический HTML, который Nginx раздаёт из `/var/www/krokosha/current`.

Статус: каркас появится в фазе 1.

```
web/
├── src/
│   ├── components/   тонкие обёртки над design/components: подставляют данные в data-slot
│   ├── layouts/      базовый layout: <head>, SEO-мета, JSON-LD, подключение tokens.css
│   ├── pages/        index, 404, privacy, thanks, play (заглушка) + языковые версии (Q1)
│   ├── lib/          загрузка content/*.yaml и generated/*.json, SEO-хелперы
│   ├── scripts/      клиентские скрипты: загрузка анимаций после LCP, ленивые пасхалки
│   ├── styles/       глобальные стили поверх design/tokens.css
│   └── i18n/         словари интерфейса (после ответа на Q1)
├── public/
│   ├── img/          аватар (AVIF/WebP, генерируется sync), OG-картинка
│   ├── fonts/        шрифты для preload
│   └── assets/       analytics.js (читаемый, < 3 КБ gzip)
└── tests/e2e/        Playwright: главная, форма, 404, админка
```
