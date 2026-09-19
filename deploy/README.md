# deploy/ — установка и эксплуатация на VPS

**Статус:** устанавливаются сайт (nginx, сертификат, сборка релизов по таймеру), MySQL и Redis в Docker, API с формой заявок, админка с первым администратором (статистика, трафик, статус системы, заявки, бот), Telegram-бот (токен — `--telegram-token-file` или вопрос без эха; проверяется через `getMe`; в конце установки печатается одноразовая ссылка для владельца), фаервол, fail2ban. Почтовый сервер добавится в этот же установщик следующим шагом; до тех пор уведомления о заявках ждут в очереди и уйдут, как только почта будет настроена ([docs/roadmap.md](../docs/roadmap.md)).

> На VPS пока ничего не выкладывается — проверяем на локальном стенде (ниже).

## Установка на чистый сервер

Ubuntu 24.04 (или Debian 12), домен уже указывает на сервер:

```bash
git clone https://github.com/DenisHumen/krokosha-site.git
sudo ./krokosha-site/deploy/install.sh --domain example.com --email admin@example.com
```

Скрипт спросит согласие с условиями Let's Encrypt (или `--agree-tos`), затем логин и пароль первого администратора (пароль — без эха, дважды, не короче 12 символов). В конце печатает адрес админки — он секретный, сохраните его. Повторный запуск ничего не ломает: он приводит сервер к тому же состоянию. Все параметры — `install.sh --help`.

| Что | Где |
|---|---|
| Код | `/opt/krokosha/repo` (клон репозитория), бинарники — `/opt/krokosha/bin` |
| Go и Node.js | `/opt/krokosha/toolchain` — официальные архивы, версии и SHA-256 закреплены в [`toolchain.env`](toolchain.env). В систему ничего не ставится, сторонние apt-репозитории не подключаются |
| Сайт | `/var/www/krokosha/releases/<дата-время>`, `current` → живой релиз (хранятся 3 последних) |
| **Данные** | `/srv/krokosha` (`--data-dir`): `mysql/`, `redis/`, `attachments/`, `backups/`, `config/`, позже `mail/`. **Переезд = скопировать эту директорию** ([architecture.md §6](../docs/architecture.md)) |
| Настройки и секреты | `/etc/krokosha` — ссылка на `/srv/krokosha/config`. `env` — `600 root`, описание в [`env/.env.example`](env/.env.example); пароль root MySQL лежит отдельно (`mysql-root.env`), API его не видит |
| MySQL и Redis | Docker Compose ([`compose/compose.yaml`](compose/compose.yaml)): слушают только `127.0.0.1`, жёсткие лимиты памяти, MySQL настроен на малую память ([`compose/mysql/krokosha.cnf`](compose/mysql/krokosha.cnf)) |
| API | `krokosha-api.service`: `127.0.0.1:8080`, за nginx (`/api/`), миграции базы применяет при старте |
| Админка | `https://<домен><ADMIN_PATH>/` — путь случайный (или `--admin-path`), хранится в `/etc/krokosha/env`. Только HTTPS. Учётные записи: `sudo krokosha-cli admin …` ([api/README.md](../api/README.md)) |
| Кэши | `/var/lib/krokosha` — npm, Go, ответы GitHub; можно удалить без потерь |
| Логи | `journalctl -u krokosha-sync.service`, `/var/log/krokosha/` (JSON access-лог сайта и отдельный — админки, 30 дней). Access-лог сайта читает API (группа `adm`) для экрана «Трафик сервера» |
| Состояние | `/var/lib/krokosha/status/sync.json` — отчёт последней сборки для экрана «Статус системы»; `/var/lib/krokosha/requests/` — сюда админка кладёт запрос «пересобрать сейчас» |
| Пользователь | `krokosha` — системный, без шелла и пароля; от него идут sync и сборка |

## Локальный стенд

Контейнер Docker, который выглядит как VPS (Ubuntu 24.04, systemd); внутри отрабатывает настоящий `install.sh`, включая Docker с MySQL и Redis.

```bash
deploy/docker/staging/staging.sh up       # установить текущую ветку (закоммиченное состояние) → https://krokosha.localhost
deploy/docker/staging/staging.sh update   # подтянуть новые коммиты ветки и применить (update.sh)
deploy/docker/staging/staging.sh test     # полный цикл, как в CI: установка, повтор, update, откат, удаление (~2,5 мин)
deploy/docker/staging/staging.sh shell    # root-консоль внутри
deploy/docker/staging/staging.sh down     # убрать стенд
```

Нужен Docker (на Windows — Docker Desktop, запускать из Git Bash или WSL). Сертификат самоподписанный: браузер предупредит один раз. WireGuard в ядре WSL2 отсутствует, поэтому эта часть проверки выполняется только в CI.

Админка стенда — `https://krokosha.localhost/_staging/`, логин `dev`; пароль случайный, `staging.sh up` печатает его в конце.

## Обслуживание

```bash
sudo /opt/krokosha/repo/deploy/update.sh              # подтянуть main и применить: код, конфиги, сборка, релиз
sudo systemctl start krokosha-sync.service            # пересобрать сейчас (само — каждые 6 часов)
sudo /opt/krokosha/repo/deploy/rollback.sh            # вернуть предыдущий релиз (--list — показать все)
sudo /opt/krokosha/repo/deploy/uninstall.sh           # удалить сайт с сервера; данные в /srv/krokosha остаются (--purge удаляет и их)
```

Сервер сам забирает код из GitHub; ключей от сервера в GitHub нет (бриф B9).

## Как устроен релиз

`krokosha-sync.timer` (00:10, 06:10, 12:10, 18:10) → [`bin/build-release.sh`](bin/build-release.sh):

1. `krokosha-cli sync` — данные GitHub. Ошибка не фатальна: сборка идёт из кэша.
2. `npm ci --omit=dev` — только если изменился `package-lock.json`.
3. `astro build` → `check-dist.mjs`. Не прошла проверка — релиз не публикуется, живой сайт не трогается.
4. Копия в `releases/<дата-время>`, атомарная смена симлинка `current`, удаление релизов старше трёх.
5. Отчёт в `status/sync.json` — чем кончилось и на каком шаге остановилось (пишется и при сбое).

Кроме таймера сборку запускает кнопка «Пересобрать сейчас» в админке: `krokosha-rebuild.path` следит за файлом-запросом и стартует тот же юнит. У веб-сервиса нет прав что-либо запускать — только положить этот файл.

npm и сборка запускаются с **чистым окружением**: секреты из `/etc/krokosha/env` (токен GitHub, позже — почта и бот) стороннему коду не видны. Сам юнит изолирован средствами systemd (`ProtectSystem=strict`, без привилегий, лимит памяти), чтобы сборка не мешала остальному на небольшом VPS.

## Nginx

- HTTP → HTTPS, `www` → основной домен, запросы с чужим именем хоста (голый IP, чужой домен) остаются без ответа.
- Страницы — `Cache-Control: no-cache` (новый релиз виден сразу), файлы `/_astro/` с хэшем в имени — год, `immutable`.
- «Страница не найдена» — на языке адреса: `/uk/…` → украинская, `/ru/…` → русская.
- Заголовки безопасности — [`nginx/snippets/krokosha-headers.conf`](nginx/snippets/krokosha-headers.conf). CSP с хэшами скриптов и стилей приходит из сборки (`<meta>`), заголовок добавляет `frame-ancestors`. HSTS — только с настоящим сертификатом.
- gzip и, если в системе есть модуль, brotli.
- Access-лог в JSON — источник для экрана «Трафик сервера» в админке.
- **Админка** проксируется только по HTTPS и только под секретным путём. Её запросы пишутся в отдельный лог (`nginx-admin.json.log`): секретный путь и клики владельца не попадают в статистику трафика. На форму входа — свой лимит: 20 запросов в минуту с адреса (проверка пароля намеренно дорогая).

## fail2ban

- `sshd` — вход по SSH только по ключам, jail просто убирает шум.
- `krokosha-admin` — неудачные входы в админку: 10 за 15 минут → бан на час (повторные — дольше, до недели), только для портов 80/443. Источник — лог админки в nginx (ответы 401 и 429 на форму входа); API полный адрес нигде не хранит.

```bash
sudo fail2ban-client status krokosha-admin              # кто забанен
sudo fail2ban-client set krokosha-admin unbanip АДРЕС   # снять бан (например, свой)
```

## Фаервол и то, что уже живёт на сервере

Установщик трогает только своё (docs/architecture.md §5):

- Правило для SSH добавляется первым и проверяется **до** включения UFW; порт берётся из `sshd -T`.
- Открываются 80 и 443; дополнительные порты — `--allow 51820/udp` (можно несколько, запоминаются).
- **WireGuard определяется автоматически:** для каждого интерфейса остаётся открытым его порт и разрешается маршрутизация клиентов (`ufw route allow in on wg0`) — по умолчанию UFW пересылаемый трафик отбрасывает. `net.ipv4.ip_forward` не меняется, NAT-правила не трогаются.
- `--skip-firewall` — не настраивать UFW вовсе.

## Проверка в CI

Job `deploy`: shellcheck всех скриптов и [`ci/test-install.sh`](ci/test-install.sh) — настоящая установка на чистой Ubuntu 24.04 (одноразовая VM GitHub) с самоподписанным сертификатом и тестовым WireGuard-интерфейсом: сайт на трёх языках, редиректы, заголовки, кэш, сжатие, база и API, аналитика, админка (вход, cookie, CSRF, лимиты, fail2ban, CLI, экраны, живая лента), трафик по логу nginx, кнопка «пересобрать» (новый релиз через path-юнит), форма заявки (обычный POST → страница «спасибо» с номером, JSON, спам, лимит, чужой сайт), заявки в админке (список, доска, карточка, «взять», запрещённый переход, ответ в очереди, заметка, шаблоны, CSV, удаление данных клиента с записью в журнале), вложения (выключены → отказ; включены в `content/site.yaml` → файлы в `/srv/krokosha/attachments` с правами `0600`, подделка типа, лишний файл, предел nginx только для `/api/leads`, выдача из админки как загрузка, удаление вместе с заявкой), Telegram-бот против притворного Bot API (`ci/mock-telegram.py`: проверка токена установщиком, webhook с секретным адресом и заголовком через nginx, вход владельца по одноразовому приглашению, повторное использование кода, токен не в логах, страница в админке), фаервол, повторный запуск, `update.sh`, откат, удаление.

## Структура

```
deploy/
├── install.sh          идемпотентная установка; update.sh вызывает её же с сохранёнными настройками
├── update.sh           git pull → install.sh --from-env
├── rollback.sh         откат на предыдущий релиз, таймер пересборки ставится на паузу
├── uninstall.sh        удаление с подтверждением
├── toolchain.env       закреплённые версии Go и Node.js с контрольными суммами
├── compose/            MySQL и Redis: compose.yaml, конфиг MySQL для малой памяти
├── docker/staging/     локальный стенд: контейнер-«VPS» и staging.sh
├── bin/                build-release.sh — sync, сборка, проверка, публикация релиза
├── lib/                common.sh — общие функции: журнал, шаблоны, /etc/krokosha/env
├── nginx/              шаблоны сайта (@@ИМЯ@@ → значение), сниппеты TLS / заголовков / сжатия, формат лога
├── systemd/            krokosha-api.service, krokosha-sync.service + .timer, krokosha-rebuild.path; позже — krokosha-certwatch
├── logrotate/          ротация access-лога: 30 дней
├── fail2ban/           jail для sshd и для входа в админку (+ фильтр); позже — почта
├── env/                .env.example — описание /etc/krokosha/env
├── ci/                 test-install.sh — интеграционный тест установщика
├── certbot/            (позже) хук: перезагрузка сертификата в почтовом контейнере
└── mailserver/         (позже) docker-mailserver: compose, конфиг, DKIM
```

Ещё не сделано из брифа B8: `backup.sh`, `krokosha-certwatch` (штатное продление уже работает через `certbot.timer`), почта и токен бота — см. [roadmap](../docs/roadmap.md).

Правила: `set -euo pipefail`, shellcheck в CI, никаких `curl | bash`, секреты только в `/etc/krokosha/env`.
