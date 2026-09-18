# Контракт дизайн ↔ бэкенд

**Версия 1.2** (2026-09-18). Основа — часть C брифа ([brief/MASTER_PROMPT.md](brief/MASTER_PROMPT.md)). Всё, что добавлено сверх брифа, помечено **[v1.1]** / **[v1.2]**.

Контракт меняется только через PR, который правит этот файл и одновременно `mock/`. Ни дизайн, ни бэкенд не меняют формат данных молча.

| Версия | Что изменилось |
|---|---|
| 1.1 | `tier` / `archived` / `stale` у проектов, `site.json`, `data-field`, новые секции и треки |
| 1.2 | **Три языка** (§6): `mock/` разложен по локалям `mock/{en,uk,ru}/`. В `site.json` добавлены `i18n`, `ui`, `not_found`, `projects.labels`, подписи/ошибки/сообщения формы. Реальные Telegram и email |

---

## 1. Зоны ответственности

| Каталог | Кто пишет | Правило |
|---|---|---|
| `design/` | Claude Design | Бэкенд не переписывает визуальные решения. Минимальные правки разметки ради подстановки данных записываются в [integration-notes.md](integration-notes.md) |
| `web/` | Backend | Тонкие обёртки над `design/components`: берут данные и заполняют `data-slot` / `data-field` |
| `content/` | Денис (+ Backend) | Источник всех текстов и данных |
| `mock/` | Backend | Снимок данных в формате этого контракта |
| `api/`, `deploy/` | Backend | — |

---

## 2. Схемы данных

Все файлы — UTF-8 JSON. **[v1.2]** Лежат по локалям: `mock/en/`, `mock/uk/`, `mock/ru/` — в каждой папке одни и те же 4 файла одной и той же формы, строки уже переведены (§6). Украинский и русский заметно длиннее английского — дизайн не должен ломаться на 1.5× длине.

### 2.1. `profile.json`

```json
{
  "name": "Denis",
  "nickname": "Krokosha",
  "role": "Network & Infrastructure Engineer · DevOps",
  "experience_years": 8,
  "avatar": { "src": "/img/avatar-256.avif", "srcset": "/img/avatar-128.avif 128w, /img/avatar-256.avif 256w, /img/avatar-512.avif 512w", "alt": "Denis (Krokosha)" },
  "location": "Ukraine",
  "socials": [
    { "id": "telegram", "label": "Telegram", "url": "https://t.me/DenisHumen", "primary": true },
    { "id": "email", "label": "Email", "url": "mailto:denis@krokosha.xyz", "primary": true },
    { "id": "github", "label": "GitHub", "url": "https://github.com/DenisHumen" }
  ]
}
```

| Поле | Тип | Примечание |
|---|---|---|
| `experience_years` | int | Считается при сборке от `career_start`. Здесь только число. Слово («лет», «года») подставляет web с учётом склонения |
| `avatar` | object | Квадрат, локальные AVIF/WebP. Размер на странице задаёт дизайн (CSS), чтобы не было CLS |
| `socials[].id` | string | `telegram`, `email`, `github`, в будущем `linkedin`, `upwork`, `x`. **Дизайн рисует иконку для каждого id из этого списка** |
| `socials[].primary` | bool? | `true` → крупная CTA-кнопка в hero и контактах; остальные — только иконки |

### 2.2. `skills.json`

Ровно 3 уровня: категория → группа → навык.

```json
[
  {
    "id": "network", "title": "Сети", "icon": "network",
    "children": [
      {
        "id": "net-hardware", "title": "Сетевое оборудование",
        "children": [
          { "id": "mikrotik", "title": "MikroTik", "note": "RouterOS", "confirmed": true }
        ]
      }
    ]
  }
]
```

| Поле | Примечание |
|---|---|
| категория `icon` | `network`, `server`, `storage`, `devops`, `os`, `code` — иконки рисует дизайн |
| навык `note` | одна строка или `null` |
| навык `confirmed` | В `mock/` лежат **все** навыки, включая `false`: так дизайн проверяется на максимальной плотности (63 навыка, из них 20 подтверждённых). В боевой сборке `false` отфильтрованы, пока в `site.yaml` не включён `flags.skills.show_unconfirmed`. Отдельного стиля для `false` не нужно |

### 2.3. `projects.json`

```json
{
  "public": [
    {
      "name": "vlb-Virtual-Load-Balancer",
      "description": "Convenient, lightweight software for balancing traffic across multiple providers",
      "language": "Rust", "languageColor": "#dea584",
      "topics": [], "stars": 0,
      "updatedAt": "2026-09-10",
      "url": "https://github.com/DenisHumen/vlb-Virtual-Load-Balancer",
      "homepage": null,
      "pinned": false,
      "tier": "featured",
      "archived": false
    }
  ],
  "private": [
    { "title": "TODO", "description": "TODO", "tags": ["Kubernetes"], "note": "NDA" }
  ],
  "syncedAt": "2026-09-18T12:00:00Z",
  "stale": false
}
```

| Поле | Тип | Примечание |
|---|---|---|
| `description` | string \| null | Если на GitHub пусто или совпадает с именем — первый абзац README (≤ 160 символов). Может быть `null` |
| `language`, `languageColor` | string \| null | У репозитория может не быть языка |
| `homepage` | string \| null | Ссылки на t.me / github.com отбрасываются (`homepage_ignore_hosts`) |
| `stars` | int | Показывать только если > 0 |
| **[v1.1]** `tier` | `"featured"` \| `"standard"` \| `"compact"` | Уровень подачи, см. §5. Показываются **все** публичные репозитории, но по-разному |
| **[v1.1]** `archived` | bool | Архивный — метка «архив», всегда `compact` |
| **[v1.1]** `stale` | bool | `true` — сборка из кэша, GitHub был недоступен. Показать `site.projects.error_note` |

Массив `public` уже отсортирован: pinned → featured → standard → compact. Порядок не менять.

Представление по tier (предложение, дизайн может улучшить):
- `featured` — крупные карточки, сетка 2–3 колонки, все поля;
- `standard` — обычные карточки поменьше: имя, описание (2 строки), язык, дата;
- `compact` — плотный список-таблица (имя · язык · дата), свёрнут под кнопкой `site.projects.show_all_label`.

### 2.4. [v1.1] `site.json`

Тексты и настройки страницы — зеркало `content/site.yaml` без профиля и соцсетей. Появился, потому что по правилу брифа ни один текст не хардкодится в разметке.

| Ключ | Что это |
|---|---|
| **[v1.2]** `i18n` | `{ locale, default, locales: [{ code, label, href }] }` — переключатель языка: `EN` → `/`, `UA` → `/uk/`, `RU` → `/ru/` |
| `nav[]` | `{ number, label, anchor }` — пункты верхней навигации |
| `hero` | `headline.muted` (серая строка) + `headline.strong` (чёрная), `lead`, `experience_label`, `captions.{left,center,right}`, `link_caption`, `cta.{telegram,email,discuss}` |
| `services[]` | `{ id, number, title, subtitle, text }` — секция с липкими номерами. `id` = якорь (`/#networks`) |
| `stats` | подписи к цифрам |
| `projects` | `heading`, `note`, `private_heading`, `private_note`, `show_all_label`, `error_note`, **[v1.2]** `labels.{archived, updated, website, source}` |
| `curtain` | `heading`, `text`, `cta` — тёмный занавес |
| `contacts` | `heading`, `text`, `form.{enabled, attachments, reply_within_hours, directions[], contact_methods[], budgets[], timelines[]}`, **[v1.2]** `form.labels.*`, `form.placeholders.*`, `form.messages.*` (успех, ошибка сервера, rate-limit), `form.errors.*` (валидация). `reply_within_hours: null` → фразу `messages.success_reply` не показывать |
| **[v1.2]** `ui` | `headings.{services, skills, stats}`, `skip_to_content`, `theme_toggle`, `language`, `skills_search`, `skills_no_results` |
| **[v1.2]** `not_found` | 404: `title`, `text`, `game_hint`, `back` |
| `footer` | **[v1.2]** `copyright`, `game_entry`, `play_stub` |
| `flags` | `easter_eggs.*` — какие пасхалки включены; дизайн проверяет флаг перед запуском |

Значения с пометкой «черновик» в YAML — рабочие тексты, их будут править. Вёрстка не должна зависеть от их точной длины.

---

## 3. Слоты данных

Место подстановки — атрибут `data-slot`. Внутри повторяющегося элемента поля помечаются **[v1.1]** `data-field`.

```html
<ul data-slot="projects.public">
  <!-- один шаблон, web размножает -->
  <li data-component="project-card" data-tier="featured">
    <a data-field="url"><h3 data-field="name"></h3></a>
    <p data-field="description"></p>
    <span data-field="language" style="--lang-color: …"></span>
  </li>
</ul>
```

| Слот | Источник |
|---|---|
| `profile.name`, `profile.nickname`, `profile.role`, `profile.experience_years`, `profile.avatar`, `profile.location` | profile.json |
| `socials` | profile.json → socials[] |
| `skills` | skills.json |
| `projects.public`, `projects.private` | projects.json |
| **[v1.1]** `site.<путь>` | site.json, например `site.hero.headline.strong`, `site.services`, `site.curtain.heading` |

Повторяющиеся элементы — **один шаблонный компонент** (`project-card`, `private-card`, `skill-node`, `social-button`, `service-block`), а не N копий.

---

## 4. Трекинг

Больше дизайну про аналитику знать ничего не нужно.

**Секции** — `data-section`: `hero`, `services`, `skills`, **[v1.1]** `stats`, `projects`, **[v1.1]** `curtain`, `contacts`, `footer`.

**Элементы** — `data-track`:

| id | Элемент |
|---|---|
| `cta-telegram`, `cta-email` | главные CTA (конверсии) |
| **[v1.1]** `cta-discuss` | «Обсудить проект» (hero, занавес) — скролл к форме |
| `social-<id>` | иконки соцсетей: `social-github`, `social-telegram`… |
| `skill-<id>` | раскрытие категории/навыка |
| `project-<name>` | клик по карточке репозитория |
| **[v1.1]** `projects-show-all` | раскрытие компактного списка |
| **[v1.1]** `form-submit` | отправка формы |
| `egg-<id>` | найдена пасхалка: `egg-konami`, `egg-sudo`, `egg-croc`… |
| `game-entry` | вход в мини-игру в футере |
| **[v1.2]** `lang-<code>` | переключение языка: `lang-en`, `lang-uk`, `lang-ru` |

---

## 5. [v1.1] Ранжирование публичных репозиториев

Решение Дениса: показывать все репозитории, проработанные — заметнее. Уровень считает `api/internal/githubsync` при каждом sync по метрикам GitHub. Формула прозрачная, коэффициенты можно менять.

| Признак | Баллы |
|---|---|
| Размер README | ≥ 10 КБ → 30 · ≥ 5 КБ → 22 · ≥ 2 КБ → 15 · ≥ 500 Б → 8 |
| Коммиты | ≥ 200 → 20 · ≥ 50 → 15 · ≥ 20 → 10 · ≥ 5 → 5 |
| Релизы | ≥ 5 → 15 · ≥ 1 → 8 |
| Есть осмысленное описание (не пустое и не равно имени) | 10 |
| Есть лицензия | 5 |
| Есть настоящий homepage (не t.me / github) | 5 |
| Есть топики | 5 |
| Звёзды | 2 за звезду, максимум 10 |
| Свежесть (`pushedAt`) | ≤ 180 дней → 10 · ≤ 365 дней → 5 |
| Архивный | −20 |

Уровни:
1. `pinned` из `content/projects.yaml` → всегда `featured`, первыми, в заданном порядке.
2. Остальные слоты `featured` (до 6 карточек вместе с pinned) — лучшие по баллам среди неархивных с баллом ≥ 55.
3. `standard` — балл ≥ 30.
4. `compact` — остальные и все архивные.
5. `overrides.<repo>.tier` в `projects.yaml` перекрывает расчёт.

Внутри уровня — по баллам, при равенстве — по `updatedAt`.

Снимок на 2026-09-18 — 22 публичных репозитория (23 минус профильный), из них 5 архивных: см. `mock/<locale>/projects.json`.

---

## 6. [v1.2] Языки

| Код | Кнопка | Адрес | `<html lang>` / `hreflang` |
|---|---|---|---|
| `en` | EN | `/` — **основной** | `en` (+ `x-default`) |
| `uk` | UA | `/uk/` | `uk` — код языка «украинский» (UA — код страны) |
| `ru` | RU | `/ru/` | `ru` |

- **Источник** (`content/*.yaml`): текстовое поле — строка (одинаково на всех языках: `MikroTik`, `GitHub`) или словарь `{ en, uk, ru }`. У словаря должны быть заполнены все три ключа; `null` допустим, если подписи в каком-то языке нет.
- **Мок** (`mock/<locale>/*.json`): словари уже разрешены в строки нужного языка — схема та же, что в §2.
- **Множественное число** разрешает сборка (правила CLDR, формы в `site.yaml → plurals`). В моке уже подставлено для 8 лет и 22 проектов: `8 years` / `8 років` / `8 лет`, `22 public projects` / `22 публічні проєкти` / `22 публичных проекта`.
- **Плейсхолдеры** в фигурных скобках остаются в строках мока и подставляются при сборке или в браузере: `{id}` (номер заявки `#K-0042`), `{hours}`, `{email}`, `{privacy_link}` (ссылка на `/privacy`), `{year}`. В превью дизайн подставляет примеры сам.
- Якоря секций (`#networks`, `#servers`, `#devops`…) и `id` элементов **одинаковы на всех языках**.
- Репозитории GitHub не переводятся: описание остаётся на языке оригинала.
