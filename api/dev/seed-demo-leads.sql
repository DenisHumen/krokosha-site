-- Demo requests for looking at the «Заявки» screens locally (api/README.md, «Посмотреть админку локально»).
-- Made-up people and companies; every status, a conversation, notes, an answer that failed to be sent.
-- Safe to run again: it replaces its own rows only (ip_prefix 192.0.2.0/24 is a documentation range).

DELETE FROM outbox WHERE lead_id IN (SELECT id FROM leads WHERE ip_prefix = '192.0.2.0/24');
DELETE FROM leads WHERE ip_prefix = '192.0.2.0/24'; -- the conversation and the history go with it

SET @now = UTC_TIMESTAMP(3);

INSERT INTO leads (public_token, created_at, updated_at, status, assignee, assigned_at, first_response_at, closed_at, reject_reason,
                   name, contact_method, contact_value, direction, description, budget, timeline, lang, consent_at, spam_score, spam_reasons,
                   source, referrer_host, utm_source, utm_medium, utm_campaign, country, device, browser, os, ip_prefix, sections_seen, time_on_site_ms)
VALUES
(UNHEX(MD5('demo-lead-1')), @now - INTERVAL 35 MINUTE, @now - INTERVAL 35 MINUTE, 'new', NULL, NULL, NULL, NULL, NULL,
 'Олена Коваль', 'telegram', '@olena_koval', 'networks',
 'Відкриваємо другий офіс на 25 місць. Потрібно з''єднати його з головним, розділити гостьовий Wi-Fi та камери, бажано на MikroTik.',
 '$1–3k', 'місяць', 'uk', @now - INTERVAL 35 MINUTE, 0, NULL,
 'search', 'google.com', NULL, NULL, NULL, 'UA', 'desktop', 'Chrome', 'Windows', '192.0.2.0/24', 'hero → services → projects → contacts', 214000),

(UNHEX(MD5('demo-lead-2')), @now - INTERVAL 3 HOUR, @now - INTERVAL 3 HOUR, 'new', NULL, NULL, NULL, NULL, NULL,
 'Mark Jensen', 'email', 'mark@northwind.example', 'devops',
 'We deploy by hand over SSH and it hurts. Looking for someone to set up CI/CD with staging and production, Docker, and sane rollbacks.',
 '$3–10k', '1–2 weeks', 'en', @now - INTERVAL 3 HOUR, 0, NULL,
 'ads', 'google.com', 'google', 'cpc', 'devops-eu', 'DK', 'desktop', 'Firefox', 'macOS', '192.0.2.0/24', 'hero → services → contacts', 96000),

(UNHEX(MD5('demo-lead-3')), @now - INTERVAL 1 DAY, @now - INTERVAL 20 HOUR, 'in_progress', 'dev', @now - INTERVAL 23 HOUR, @now - INTERVAL 22 HOUR, NULL, NULL,
 'Андрей Соколов', 'email', 'a.sokolov@stroymir.example', 'servers',
 'Два сервера Dell под виртуализацию и СХД на 12 дисков. Нужно спроектировать кластер Proxmox с отказоустойчивостью и бэкапами.',
 '$3–10k', 'месяц', 'ru', @now - INTERVAL 1 DAY, 0, NULL,
 'direct', NULL, NULL, NULL, NULL, 'UA', 'desktop', 'Chrome', 'Windows', '192.0.2.0/24', 'hero → skills → projects → contacts', 388000),

(UNHEX(MD5('demo-lead-4')), @now - INTERVAL 2 DAY, @now - INTERVAL 5 HOUR, 'in_progress', 'dev', @now - INTERVAL 47 HOUR, @now - INTERVAL 46 HOUR, NULL, NULL,
 'Ірина Мельник', 'email', 'iryna@kavarnia.example', 'networks',
 'Мережа з трьох кав''ярень: каси, Wi-Fi для гостей, відеонагляд. Постійно відвалюється зв''язок з касами, хочемо навести лад.',
 'до $500', 'терміново', 'uk', @now - INTERVAL 2 DAY, 0, NULL,
 'social', 'instagram.com', NULL, NULL, NULL, 'UA', 'mobile', 'Safari', 'iOS', '192.0.2.0/24', 'hero → services → contacts', 143000),

(UNHEX(MD5('demo-lead-5')), @now - INTERVAL 4 DAY, @now - INTERVAL 3 DAY, 'waiting_client', 'dev', @now - INTERVAL 95 HOUR, @now - INTERVAL 94 HOUR, NULL, NULL,
 'Sofia Lindgren', 'email', 'sofia@fjordlabs.example', 'development',
 'A small internal dashboard in Go: reads metrics from our switches over SNMP and shows port load. Around 40 devices.',
 '$1–3k', 'no rush', 'en', @now - INTERVAL 4 DAY, 0, NULL,
 'search', 'duckduckgo.com', NULL, NULL, NULL, 'SE', 'desktop', 'Firefox', 'Linux', '192.0.2.0/24', 'hero → projects → skills → contacts', 421000),

(UNHEX(MD5('demo-lead-6')), @now - INTERVAL 9 DAY, @now - INTERVAL 2 DAY, 'done', 'dev', @now - INTERVAL 213 HOUR, @now - INTERVAL 212 HOUR, @now - INTERVAL 2 DAY, NULL,
 'Виктор Гаврилюк', 'phone', '+380501234567', 'highload-lan',
 'Компьютерный клуб на 60 мест: бездисковая загрузка, 10G до сервера, игры с одного образа. Нужен проект и настройка.',
 '$3–10k', '1–2 недели', 'ru', @now - INTERVAL 9 DAY, 0, NULL,
 'ads', 'google.com', 'google', 'cpc', 'lan-clubs', 'UA', 'mobile', 'Chrome', 'Android', '192.0.2.0/24', 'hero → services → projects → contacts', 267000),

(UNHEX(MD5('demo-lead-7')), @now - INTERVAL 12 DAY, @now - INTERVAL 11 DAY, 'done', 'dev', @now - INTERVAL 287 HOUR, @now - INTERVAL 286 HOUR, @now - INTERVAL 11 DAY, NULL,
 'Natalia Bondar', 'telegram', '@nbondar', 'devops',
 'Перенести два сайта и базу с shared-хостинга на VPS: Docker, nginx, сертификаты, бэкапы по расписанию.',
 '$500–1k', '1–2 недели', 'ru', @now - INTERVAL 12 DAY, 0, NULL,
 'direct', NULL, NULL, NULL, NULL, 'PL', 'desktop', 'Chrome', 'Windows', '192.0.2.0/24', 'hero → services → contacts', 118000),

(UNHEX(MD5('demo-lead-8')), @now - INTERVAL 6 DAY, @now - INTERVAL 6 DAY + INTERVAL 2 HOUR, 'rejected', 'dev', NULL, @now - INTERVAL 6 DAY + INTERVAL 2 HOUR, @now - INTERVAL 6 DAY + INTERVAL 2 HOUR, 'не моя область',
 'Tom Becker', 'email', 'tom@brightapps.example', 'development',
 'We need an iOS and Android app for our delivery service with a designer-made UI and App Store publishing.',
 'over $10k', 'a month', 'en', @now - INTERVAL 6 DAY, 0, NULL,
 'other', 'news.ycombinator.com', NULL, NULL, NULL, 'DE', 'desktop', 'Safari', 'macOS', '192.0.2.0/24', 'hero → contacts', 52000),

(UNHEX(MD5('demo-lead-9')), @now - INTERVAL 5 HOUR, @now - INTERVAL 5 HOUR, 'spam', NULL, NULL, NULL, NULL, NULL,
 'SEO Expert', 'email', 'promo@rank-first.example', 'other',
 'Buy cheap traffic and backlinks for your website today! Guaranteed first page, casino and crypto friendly. http://rank-first.example http://cheap-seo.example',
 NULL, NULL, 'en', @now - INTERVAL 5 HOUR, 100, 'без проверки proof-of-work (отправлено без JavaScript); ссылки в описании; стоп-слово «backlinks»',
 NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, '192.0.2.0/24', NULL, NULL),

(UNHEX(MD5('demo-lead-10')), @now - INTERVAL 16 DAY, @now - INTERVAL 15 DAY, 'done', 'dev', @now - INTERVAL 383 HOUR, @now - INTERVAL 382 HOUR, @now - INTERVAL 15 DAY, NULL,
 'Дмитро Шевчук', 'email', 'd.shevchuk@agroland.example', 'servers',
 'Файловий сервер для бухгалтерії та 1С: RAID, резервні копії на зовнішній диск і в хмару, доступ через VPN.',
 '$500–1k', 'не горить', 'uk', @now - INTERVAL 16 DAY, 0, NULL,
 'search', 'google.com', NULL, NULL, NULL, 'UA', 'desktop', 'Edge', 'Windows', '192.0.2.0/24', 'hero → services → skills → contacts', 305000);

-- Each request belongs to one of the demo visits (seed-demo.sql), so that «Путь по сайту» leads somewhere.
-- The robot's one does not: robots send the form without ever loading the page's scripts.
UPDATE leads l
JOIN (SELECT id, ROW_NUMBER() OVER (ORDER BY id) AS n FROM leads WHERE ip_prefix = '192.0.2.0/24' AND status <> 'spam') numbered ON numbered.id = l.id
JOIN (SELECT session_id, ROW_NUMBER() OVER (ORDER BY MIN(id)) AS n FROM analytics_pageviews WHERE ip_prefix = '192.0.2.0/24' GROUP BY session_id) visit
  ON visit.n = numbered.n * 7
SET l.session_id = visit.session_id;

-- What every request starts with: the visitor's own text, and the «created» line of its history.
INSERT INTO lead_messages (lead_id, created_at, direction, channel, author, body)
SELECT id, created_at, 'in', 'form', NULL, description FROM leads WHERE ip_prefix = '192.0.2.0/24';

INSERT INTO lead_events (lead_id, created_at, actor, action, from_status, to_status, details)
SELECT id, created_at, 'client', 'created', NULL, IF(status = 'spam', 'spam', 'new'), NULL FROM leads WHERE ip_prefix = '192.0.2.0/24';

INSERT INTO lead_events (lead_id, created_at, actor, action, from_status, to_status, details)
SELECT id, assigned_at, assignee, 'assigned', 'new', 'in_progress', NULL FROM leads WHERE ip_prefix = '192.0.2.0/24' AND assigned_at IS NOT NULL;

-- Conversations.
SET @sokolov = (SELECT id FROM leads WHERE public_token = UNHEX(MD5('demo-lead-3')));
SET @melnyk = (SELECT id FROM leads WHERE public_token = UNHEX(MD5('demo-lead-4')));
SET @lindgren = (SELECT id FROM leads WHERE public_token = UNHEX(MD5('demo-lead-5')));
SET @havryliuk = (SELECT id FROM leads WHERE public_token = UNHEX(MD5('demo-lead-6')));
SET @becker = (SELECT id FROM leads WHERE public_token = UNHEX(MD5('demo-lead-8')));

INSERT INTO lead_messages (lead_id, created_at, direction, channel, author, body, delivery) VALUES
(@sokolov, @now - INTERVAL 22 HOUR, 'out', 'email', 'dev',
 'Андрей, здравствуйте! Задача понятная. Подскажите модели серверов и сколько виртуальных машин планируется — прикину схему кластера и бэкапов.', 'sent'),
(@sokolov, @now - INTERVAL 21 HOUR, 'note', 'admin', 'dev', 'Судя по описанию — R740 + полка. Уточнить про 10G между узлами.', NULL),
(@sokolov, @now - INTERVAL 20 HOUR, 'in', 'email', NULL,
 'Добрый день! Два Dell R740, виртуалок около 15, из них три тяжёлые (1С, SQL, файловый). Сеть между серверами пока 1G, готовы докупить 10G.', NULL),

(@melnyk, @now - INTERVAL 46 HOUR, 'out', 'email', 'dev',
 'Ірино, добрий день! Схоже на проблему з живленням або кабелем до кас. Яке зараз обладнання стоїть у кав''ярнях?', 'sent'),
(@melnyk, @now - INTERVAL 30 HOUR, 'in', 'email', NULL, 'Добрий день! Скрізь звичайні домашні роутери TP-Link, каси підключені по Wi-Fi.', NULL),
(@melnyk, @now - INTERVAL 5 HOUR, 'out', 'email', 'dev',
 'Дякую, тоді причина зрозуміла: каси краще перевести на кабель і поставити нормальний маршрутизатор. Надішлю варіант з цінами сьогодні.', 'failed'),

(@lindgren, @now - INTERVAL 94 HOUR, 'out', 'email', 'dev',
 'Hi Sofia! Sounds like a good fit. Which switch vendors do you have, and should the dashboard keep history or only show the current load?', 'sent'),

(@havryliuk, @now - INTERVAL 212 HOUR, 'note', 'admin', 'dev', 'Позвонил: клуб в Одессе, помещение уже есть, открытие через месяц.', NULL),
(@havryliuk, @now - INTERVAL 2 DAY, 'note', 'admin', 'dev', 'Проект сдан, образ и сервер настроены. Договорились о поддержке раз в месяц.', NULL),

(@becker, @now - INTERVAL 6 DAY + INTERVAL 2 HOUR, 'out', 'email', 'dev',
 'Hi Tom, thank you for reaching out! Mobile apps are outside my field — I work with networks, servers and DevOps, so I would not be the right person here.', 'sent');

INSERT INTO lead_events (lead_id, created_at, actor, action, from_status, to_status, details) VALUES
(@lindgren, @now - INTERVAL 94 HOUR, 'dev', 'status', 'in_progress', 'waiting_client', NULL),
(@havryliuk, @now - INTERVAL 2 DAY, 'dev', 'status', 'in_progress', 'done', NULL),
(@becker, @now - INTERVAL 6 DAY + INTERVAL 2 HOUR, 'dev', 'status', 'new', 'rejected', 'не моя область');

INSERT INTO lead_events (lead_id, created_at, actor, action, from_status, to_status, details)
SELECT id, closed_at, 'dev', 'status', 'in_progress', 'done', NULL FROM leads
WHERE ip_prefix = '192.0.2.0/24' AND status = 'done' AND id <> @havryliuk;
