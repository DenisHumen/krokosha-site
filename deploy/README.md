# deploy/ — установка и эксплуатация на VPS

Статус: скрипты появятся в фазе 1.

```
deploy/
├── install.sh          идемпотентная установка «с нуля»: sudo ./install.sh --domain … --email … [--admin-path …] [--no-mail]
├── update.sh           git pull → сборка → атомарная смена релиза → миграции
├── backup.sh           БД + почта + конфиги, с ротацией
├── rollback.sh         откат на предыдущий релиз
├── uninstall.sh        удаление с подтверждением
├── lib/                общие bash-функции (логирование, проверки, шаблонизация)
├── nginx/              конфиги сайта, API, админки; rate-limit; JSON access-лог
├── systemd/            krokosha-api.service, krokosha-sync.{service,timer}, krokosha-certwatch.{service,timer}
├── certbot/            deploy-hook: reload nginx + перезагрузка сертификата в почтовом контейнере
├── mailserver/         docker-mailserver: compose, конфиг, DKIM
├── fail2ban/           джейлы: sshd, nginx-admin, postfix/dovecot
└── env/                .env.example — описание всех переменных /etc/krokosha/env (без значений)
```

Правила: `set -euo pipefail`, shellcheck в CI, никаких `curl | bash` со сторонних источников, секреты только в `/etc/krokosha/env` (600, root:krokosha).
