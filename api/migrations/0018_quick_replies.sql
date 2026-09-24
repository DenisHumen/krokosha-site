-- Quick answers (docs/quick-replies.md): the templates become a pool the card of a request picks
-- from by context — what the client asked, where the conversation is, what was sent already — and a
-- template may carry photos, videos and documents that go to the client with the text.
--
-- category  — what the template answers (pricing, portfolio…), for the lists of the admin area;
-- keywords  — stems and phrases of the client's words, comma-separated: «цен, стоим, сколько»;
-- directions— the directions of the form it is for, comma-separated; '' — any;
-- moment    — when it fits: any | first (nothing answered yet) | talk | waiting (the client is
--             silent) | done;
-- used_*    — how often and when it was sent: a tie goes to the familiar one.
-- MySQL has no «ADD COLUMN IF NOT EXISTS», and a migration must be safe to run twice.
SET @add_columns = (
    SELECT IF(COUNT(*) = 0,
        'ALTER TABLE reply_templates
            ADD COLUMN category   VARCHAR(16)   NOT NULL DEFAULT '''' AFTER title,
            ADD COLUMN keywords   VARCHAR(1000) NOT NULL DEFAULT '''' AFTER body,
            ADD COLUMN directions VARCHAR(255)  NOT NULL DEFAULT '''' AFTER keywords,
            ADD COLUMN moment     VARCHAR(8)    NOT NULL DEFAULT ''any'' AFTER directions,
            ADD COLUMN used_count INT UNSIGNED  NOT NULL DEFAULT 0 AFTER moment,
            ADD COLUMN used_at    DATETIME(3)   NULL AFTER used_count',
        'DO 0')
    FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = 'reply_templates' AND column_name = 'keywords'
);
PREPARE add_columns FROM @add_columns;
EXECUTE add_columns;
DEALLOCATE PREPARE add_columns;

-- The files of a template: kept under random names next to the files of requests
-- (<data>/attachments/templates), for the service's eyes only. An answer made from the template gets
-- its own copy (a hard link), so that deleting the template or the request takes nothing from the other.
CREATE TABLE IF NOT EXISTS template_media (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    template_id BIGINT UNSIGNED NOT NULL,
    position    INT             NOT NULL DEFAULT 0,
    created_at  DATETIME(3)     NOT NULL,
    filename    VARCHAR(255)    NOT NULL,                        -- as the owner named it, cleaned
    kind        VARCHAR(8)      NOT NULL,                        -- jpg | png | mp4 | pdf | docx | txt — by signature
    size        INT UNSIGNED    NOT NULL,
    sha256      BINARY(32)      NOT NULL,
    stored_as   CHAR(32)        NOT NULL,                        -- the random name on disk
    UNIQUE KEY uq_template_media_stored (stored_as),
    KEY idx_template_media (template_id, position),
    CONSTRAINT fk_template_media_template FOREIGN KEY (template_id) REFERENCES reply_templates (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- An answer remembers the templates it was made from (the card does not offer them again), and how
-- many of its parts reached Telegram: a retry after a failure sends only the rest.
SET @add_templates = (
    SELECT IF(COUNT(*) = 0,
        'ALTER TABLE lead_messages
            ADD COLUMN templates  VARCHAR(255)      NULL AFTER body,
            ADD COLUMN sent_parts SMALLINT UNSIGNED NOT NULL DEFAULT 0 AFTER templates',
        'DO 0')
    FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = 'lead_messages' AND column_name = 'templates'
);
PREPARE add_templates FROM @add_templates;
EXECUTE add_templates;
DEALLOCATE PREPARE add_templates;

-- What Telegram calls a file once it has it: the same photo goes to the next client without being
-- uploaded again. By content, so a copy of a template's photo in a request finds it too.
CREATE TABLE IF NOT EXISTS telegram_uploads (
    sha256     BINARY(32)   NOT NULL,
    kind       VARCHAR(8)   NOT NULL,
    file_id    VARCHAR(255) NOT NULL,
    updated_at DATETIME(3)  NOT NULL,
    PRIMARY KEY (sha256, kind)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The templates of 0006, as long as the owner has not renamed them: what they answer and when.
UPDATE reply_templates SET category = 'greeting', moment = 'first',
       keywords = 'здравств, привет, добрый день, добрый вечер, доброе утро, вітаю, доброго дня, добрий день, привіт, hello, hi, good morning, good afternoon'
WHERE kind = 'reply' AND title IN ('Изучу и отвечу сегодня', 'Вивчу й відповім сьогодні', 'I''ll look into it today') AND category = '';
UPDATE reply_templates SET category = 'details',
       keywords = 'не знаю, не уверен, подскажите, посоветуйте, что нужно, не впевнен, порадьте, підкажіть, not sure, advise, recommend, what do you need'
WHERE kind = 'reply' AND title IN ('Нужны детали', 'Потрібні деталі', 'I need some details') AND category = '';
UPDATE reply_templates SET category = 'call',
       keywords = 'созвон, звон, позвон, поговор, обсуд, встреч, zoom, meet, teams, дзвін, зателефон, зідзвон, обговор, зустріч, call, talk, discuss, meeting'
WHERE kind = 'reply' AND title IN ('Давайте созвонимся', 'Зідзвонімося', 'Let''s have a call') AND category = '';
UPDATE reply_templates SET category = 'offer', moment = 'talk',
       keywords = 'кп, коммерческ, предложен, смет, комерційн, пропозиц, кошторис, proposal, quote, offer, estimate'
WHERE kind = 'reply' AND title IN ('Коммерческое предложение', 'Комерційна пропозиція', 'Proposal attached') AND category = '';

-- The pool: short blocks without a greeting of their own, so that several go into one answer —
-- «Приветствие» + «Стоимость» + «Сроки». Added once, after the owner's templates of the same
-- language; one with the same title is not added twice. {name} — the client, {id} — the request,
-- {site} — the site in the client's language, {account} — the request in the personal account.
INSERT INTO reply_templates (kind, lang, position, title, category, body, keywords, directions, moment, updated_at)
SELECT seed.kind, seed.lang,
       (SELECT COALESCE(MAX(t.position), 0) FROM reply_templates t WHERE t.kind = seed.kind AND t.lang = seed.lang) + seed.position,
       seed.title, seed.category, seed.body, seed.keywords, '', seed.moment, UTC_TIMESTAMP(3)
FROM (
    -- ru
    SELECT 'reply' AS kind, 'ru' AS lang, 1 AS position, 'Приветствие' AS title, 'greeting' AS category, 'first' AS moment,
           'Здравствуйте, {name}! Спасибо, что написали: заявку {id} получил и уже изучаю.' AS body,
           'здравств, привет, добрый день, добрый вечер, доброе утро' AS keywords
    UNION ALL SELECT 'reply', 'ru', 2, 'Чем я занимаюсь', 'about', 'any',
           'Коротко обо мне: я сетевой и инфраструктурный инженер и DevOps, в профессии с 2017 года. Строю и перестраиваю сети для офисов и объектов, собираю и настраиваю серверы и хранилища, контейнеризирую и автоматизирую инфраструктуру. Подробнее — на сайте: {site}',
           'кто вы, о вас, о себе, чем занимаетесь, чем вы занимаетесь, компани, команд, фрилансер, частное лицо'
    UNION ALL SELECT 'reply', 'ru', 3, 'Что я умею', 'skills', 'any',
           'С чем работаю:
— сети и сетевое оборудование (Cisco, Arista, Juniper, Extreme, Fortinet, MikroTik, Ubiquiti): настройка маршрутизаторов и коммутаторов, построение и перестройка сетей;
— серверы, СХД и кластеры (Dell, HPE, Supermicro, Fujitsu): подбор, сборка и настройка;
— DevOps и контейнеры (Docker, Kubernetes, Nginx, F5 BIG-IP): контейнеризация, оркестрация, балансировка нагрузки;
— высоконагруженные ЛВС: проектирование под большую нагрузку и скорости;
— свои инструменты и автоматизация (Python, Rust, TypeScript, Shell).',
           'умеете, умеешь, умения, навык, стек, технолог, опыт, работаете с, работаешь с, знаете, знаком, специализ, с чем работаете'
    UNION ALL SELECT 'reply', 'ru', 4, 'Примеры работ', 'portfolio', 'any',
           'Примеры работ — в разделе «Проекты» на сайте: {site}#projects. Большая часть проектов коммерческие и под NDA, поэтому код открыт не везде; о них могу подробно рассказать на созвоне.',
           'портфолио, пример, кейс, проект, делали, делал, работы, отзыв, рекомендац, гитхаб, github, что уже'
    UNION ALL SELECT 'reply', 'ru', 5, 'Стоимость', 'pricing', 'any',
           'По стоимости: цена зависит от объёма и сложности, поэтому сначала оцениваю задачу. Опишите, что есть сейчас и какой результат нужен, — пришлю оценку с разбивкой по этапам. Для крупных задач удобнее короткий созвон.',
           'цен, стоим, сколько стоит, сколько будет, сколько возьм, стоить, бюджет, прайс, расценк, смет, тариф, почем, дорог, дешев'
    UNION ALL SELECT 'reply', 'ru', 6, 'Сроки', 'timeline', 'any',
           'По срокам: точный срок назову после оценки — он зависит от объёма работ и от того, как быстро будут доступы и оборудование. Если есть дедлайн, напишите его — спланирую работу под него.',
           'срок, когда, сколько времени, как быстро, быстро, срочн, дедлайн, успеете, успеешь, за сколько, к какому числу, горит'
    UNION ALL SELECT 'reply', 'ru', 7, 'Смогу помочь', 'help', 'any',
           'Да, с такой задачей помогу. Чтобы предложить решение и оценить сроки, расскажите чуть подробнее: что есть сейчас, что должно получиться и есть ли ограничения по бюджету или срокам.',
           'сможете, можете помочь, поможете, помогите, возьметесь, беретесь, реально ли, получится ли, возможно ли, можно ли, справитесь'
    UNION ALL SELECT 'reply', 'ru', 8, 'Варианты решения', 'options', 'talk',
           'Что можно сделать по вашей задаче:
1. —
2. —
3. —
Какой вариант ближе? Могу расписать любой подробнее — со сроками и стоимостью.',
           'что можно сделать, какие варианты, вариант, как лучше, что посоветуете, что предложите, как сделать, решение'
    UNION ALL SELECT 'reply', 'ru', 9, 'Как я работаю', 'process', 'any',
           'Как проходит работа:
1. Обсуждаем задачу, я даю оценку.
2. Согласовываем объём, сроки и стоимость.
3. Делаю работу и держу вас в курсе.
4. Сдаю результат с описанием и доступами.
5. Если нужно — остаюсь на поддержке.',
           'как работаете, как вы работаете, этап, процесс, порядок, как начать, с чего начать, что дальше, как это будет'
    UNION ALL SELECT 'reply', 'ru', 10, 'Оплата и документы', 'payment', 'any',
           'Условия оплаты обсудим вместе: этапы, способ оплаты и документы подберём так, как удобно вам. Напишите, какой вариант нужен вашей бухгалтерии.',
           'оплат, счет, предоплат, аванс, договор, акт, документ, безнал, карт, крипт, фоп, реквизит, как платить'
    UNION ALL SELECT 'reply', 'ru', 11, 'Полезное', 'contacts', 'any',
           'Полезное:
— сайт и примеры работ: {site}
— номер вашей заявки: {id}
— статус и вся переписка — в личном кабинете: {account}
— мой часовой пояс — Киев (UTC+2, летом UTC+3).',
           'контакт, телефон, почта, как связаться, связаться, часы работы, график, на связи, выходн, часовой пояс, кабинет, статус'
    UNION ALL SELECT 'reply', 'ru', 12, 'Поддержка после работ', 'support', 'any',
           'После сдачи работы не пропадаю: могу взять систему на поддержку — мониторинг, обновления, помощь, если что-то сломается. Формат и стоимость поддержки обсудим отдельно.',
           'поддержк, сопровожд, обслужива, после сдачи, после запуска, гарант, если сломается, мониторинг, администрир'
    UNION ALL SELECT 'reply', 'ru', 13, 'Конфиденциальность', 'nda', 'any',
           'Готов подписать NDA. Доступы передавайте удобным защищённым способом; после окончания работ их лучше сменить — так надёжнее.',
           'nda, конфиденциал, неразглаш, соглашени, доступ, пароль, пароли, безопасн, секрет'
    UNION ALL SELECT 'reply', 'ru', 14, 'Напомнить о себе', 'followup', 'waiting',
           '{name}, напомню о заявке {id}: жду вашего ответа, чтобы двигаться дальше. Если планы поменялись — тоже напишите, закрою заявку.',
           ''
    UNION ALL SELECT 'reply', 'ru', 15, 'Спасибо за заказ', 'thanks', 'done',
           'Спасибо за заказ, {name}! Было приятно поработать. Если понадобится что-то ещё — пишите в любое время: постоянным клиентам действует скидка.',
           'спасибо, благодар, отлично, все работает, всё работает, принято'

    -- uk
    UNION ALL SELECT 'reply', 'uk', 1, 'Привітання', 'greeting', 'first',
           'Вітаю, {name}! Дякую, що написали: заявку {id} отримав і вже вивчаю.',
           'вітаю, привіт, добрий день, доброго дня, добрий вечір, доброго ранку, здрастуйте'
    UNION ALL SELECT 'reply', 'uk', 2, 'Чим я займаюся', 'about', 'any',
           'Коротко про мене: я мережевий та інфраструктурний інженер і DevOps, у професії з 2017 року. Будую й перебудовую мережі для офісів і об''єктів, збираю й налаштовую сервери та сховища, контейнеризую й автоматизую інфраструктуру. Детальніше — на сайті: {site}',
           'хто ви, про вас, про себе, чим займаєтесь, чим ви займаєтесь, компані, команд, фрилансер, приватна особа'
    UNION ALL SELECT 'reply', 'uk', 3, 'Що я вмію', 'skills', 'any',
           'З чим працюю:
— мережі та мережеве обладнання (Cisco, Arista, Juniper, Extreme, Fortinet, MikroTik, Ubiquiti): налаштування маршрутизаторів і комутаторів, побудова та перебудова мереж;
— сервери, СЗД і кластери (Dell, HPE, Supermicro, Fujitsu): підбір, збирання та налаштування;
— DevOps і контейнери (Docker, Kubernetes, Nginx, F5 BIG-IP): контейнеризація, оркестрація, балансування навантаження;
— високонавантажені локальні мережі: проєктування під велике навантаження та швидкості;
— власні інструменти й автоматизація (Python, Rust, TypeScript, Shell).',
           'вмієте, вмієш, вміння, навич, стек, технолог, досвід, працюєте з, працюєш з, знаєте, знайом, спеціаліз, з чим працюєте'
    UNION ALL SELECT 'reply', 'uk', 4, 'Приклади робіт', 'portfolio', 'any',
           'Приклади робіт — у розділі «Проєкти» на сайті: {site}#projects. Більшість проєктів комерційні й під NDA, тому код відкритий не скрізь; про них можу докладно розповісти на дзвінку.',
           'портфоліо, приклад, кейс, проєкт, проект, робили, робив, роботи, відгук, рекомендац, github, гітхаб'
    UNION ALL SELECT 'reply', 'uk', 5, 'Вартість', 'pricing', 'any',
           'Щодо вартості: ціна залежить від обсягу й складності, тому спершу оцінюю задачу. Опишіть, що є зараз і який результат потрібен, — надішлю оцінку з розбивкою на етапи. Для великих задач зручніший короткий дзвінок.',
           'ціна, ціну, ціни, вартіст, скільки коштує, скільки буде, скільки візьмете, коштуват, бюджет, прайс, розцінк, кошторис, тариф, дорого, дешев'
    UNION ALL SELECT 'reply', 'uk', 6, 'Терміни', 'timeline', 'any',
           'Щодо термінів: точний термін назву після оцінки — він залежить від обсягу робіт і від того, як швидко будуть доступи та обладнання. Якщо є дедлайн, напишіть його — сплануємо роботу під нього.',
           'термін, строк, коли, скільки часу, як швидко, швидко, терміново, дедлайн, встигнете, встигнеш, за скільки, до якого числа, горить'
    UNION ALL SELECT 'reply', 'uk', 7, 'Зможу допомогти', 'help', 'any',
           'Так, з такою задачею допоможу. Щоб запропонувати рішення й оцінити терміни, розкажіть трохи докладніше: що є зараз, що має вийти і чи є обмеження щодо бюджету або термінів.',
           'зможете, можете допомогти, допоможете, допоможіть, візьметесь, берете, чи реально, чи вийде, чи можливо, чи можна, впораєтесь'
    UNION ALL SELECT 'reply', 'uk', 8, 'Варіанти рішення', 'options', 'talk',
           'Що можна зробити з вашою задачею:
1. —
2. —
3. —
Який варіант ближчий? Можу розписати будь-який докладніше — з термінами й вартістю.',
           'що можна зробити, які варіанти, варіант, як краще, що порадите, що запропонуєте, як зробити, рішення'
    UNION ALL SELECT 'reply', 'uk', 9, 'Як я працюю', 'process', 'any',
           'Як проходить робота:
1. Обговорюємо задачу, я даю оцінку.
2. Погоджуємо обсяг, терміни й вартість.
3. Роблю роботу й тримаю вас у курсі.
4. Здаю результат з описом і доступами.
5. Якщо потрібно — залишаюся на підтримці.',
           'як працюєте, як ви працюєте, етап, процес, порядок, як почати, з чого почати, що далі, як це буде'
    UNION ALL SELECT 'reply', 'uk', 10, 'Оплата й документи', 'payment', 'any',
           'Умови оплати обговоримо разом: етапи, спосіб оплати й документи підберемо так, як зручно вам. Напишіть, який варіант потрібен вашій бухгалтерії.',
           'оплат, рахунок, передоплат, аванс, договір, акт, документ, безготів, карт, крипт, фоп, реквізит, як платити'
    UNION ALL SELECT 'reply', 'uk', 11, 'Корисне', 'contacts', 'any',
           'Корисне:
— сайт і приклади робіт: {site}
— номер вашої заявки: {id}
— статус і все листування — в особистому кабінеті: {account}
— мій часовий пояс — Київ (UTC+2, влітку UTC+3).',
           'контакт, телефон, пошта, як зв''язатися, зв''язатися, години роботи, графік, на зв''язку, вихідн, часовий пояс, кабінет, статус'
    UNION ALL SELECT 'reply', 'uk', 12, 'Підтримка після робіт', 'support', 'any',
           'Після здачі роботи не зникаю: можу взяти систему на підтримку — моніторинг, оновлення, допомога, якщо щось зламається. Формат і вартість підтримки обговоримо окремо.',
           'підтримк, супровід, обслуговуван, після здачі, після запуску, гаранті, якщо зламається, моніторинг, адміністру'
    UNION ALL SELECT 'reply', 'uk', 13, 'Конфіденційність', 'nda', 'any',
           'Готовий підписати NDA. Доступи передавайте зручним захищеним способом; після завершення робіт їх краще змінити — так надійніше.',
           'nda, конфіденцій, нерозголош, угод, доступ, пароль, паролі, безпек, секрет'
    UNION ALL SELECT 'reply', 'uk', 14, 'Нагадати про себе', 'followup', 'waiting',
           '{name}, нагадаю про заявку {id}: чекаю на вашу відповідь, щоб рухатися далі. Якщо плани змінилися — теж напишіть, закрию заявку.',
           ''
    UNION ALL SELECT 'reply', 'uk', 15, 'Дякую за замовлення', 'thanks', 'done',
           'Дякую за замовлення, {name}! Було приємно попрацювати. Якщо знадобиться ще щось — пишіть будь-коли: постійним клієнтам діє знижка.',
           'дякую, спасибі, вдячн, чудово, все працює, прийнято'

    -- en
    UNION ALL SELECT 'reply', 'en', 1, 'Greeting', 'greeting', 'first',
           'Hello {name}, thank you for reaching out: I''ve got your request {id} and I''m looking into it.',
           'hello, hi, good morning, good afternoon, good evening, dear'
    UNION ALL SELECT 'reply', 'en', 2, 'What I do', 'about', 'any',
           'A few words about me: I''m a network and infrastructure engineer and DevOps, in the field since 2017. I build and rebuild networks for offices and sites, assemble and configure servers and storage, containerize and automate infrastructure. More on the site: {site}',
           'who are you, about you, about yourself, what do you do, company, team, freelanc, agency, individual'
    UNION ALL SELECT 'reply', 'en', 3, 'What I can do', 'skills', 'any',
           'What I work with:
— networks and network hardware (Cisco, Arista, Juniper, Extreme, Fortinet, MikroTik, Ubiquiti): router and switch configuration, building and rebuilding networks;
— servers, storage and clusters (Dell, HPE, Supermicro, Fujitsu): selection, assembly and configuration;
— DevOps and containers (Docker, Kubernetes, Nginx, F5 BIG-IP): containerization, orchestration, load balancing;
— high-load LANs: design for heavy load and high speeds;
— tooling and automation of my own (Python, Rust, TypeScript, Shell).',
           'skill, stack, technolog, experience, work with, familiar, specializ, expertise, what can you do'
    UNION ALL SELECT 'reply', 'en', 4, 'Examples of work', 'portfolio', 'any',
           'You can find examples in the Projects section of the site: {site}#projects. Most of my projects are commercial and under NDA, so the code isn''t always public — I''m happy to walk you through them on a call.',
           'portfolio, example, case, project, done before, previous work, references, review, github, what have you'
    UNION ALL SELECT 'reply', 'en', 5, 'Pricing', 'pricing', 'any',
           'About the price: it depends on the scope and complexity, so I estimate the task first. Tell me what you have now and what result you need, and I''ll send an estimate broken down by stages. For larger tasks a short call works best.',
           'price, pricing, cost, how much, budget, rate, quote, fee, charge, expensive, cheap'
    UNION ALL SELECT 'reply', 'en', 6, 'Timeline', 'timeline', 'any',
           'About the timeline: I''ll give you an exact date after the estimate — it depends on the scope and on how soon the access and the hardware are ready. If you have a deadline, let me know and I''ll plan the work around it.',
           'deadline, how long, when, timeline, time frame, urgent, asap, by when, how soon, quickly'
    UNION ALL SELECT 'reply', 'en', 7, 'I can help', 'help', 'any',
           'Yes, I can help with this. To suggest a solution and estimate the time, tell me a bit more: what you have now, what should come out of it, and whether there are limits on the budget or the timeline.',
           'can you, could you, help, are you able, is it possible, possible, take on, manage, would you'
    UNION ALL SELECT 'reply', 'en', 8, 'Options', 'options', 'talk',
           'What can be done about your task:
1. —
2. —
3. —
Which one is closer to what you need? I can describe any of them in detail — with the time and the cost.',
           'what can be done, options, option, alternatives, best way, what would you suggest, recommend, how to do, solution'
    UNION ALL SELECT 'reply', 'en', 9, 'How I work', 'process', 'any',
           'How the work goes:
1. We discuss the task, I give an estimate.
2. We agree on the scope, the time and the cost.
3. I do the work and keep you posted.
4. I hand over the result with a description and the access.
5. If needed, I stay on for support.',
           'how do you work, process, steps, stages, how to start, get started, what next, how will it go'
    UNION ALL SELECT 'reply', 'en', 10, 'Payment and documents', 'payment', 'any',
           'We''ll agree on the payment terms together: the stages, the way to pay and the documents can be whatever suits you. Let me know what your accounting needs.',
           'payment, pay, invoice, prepay, advance, deposit, contract, card, bank transfer, wire, crypto, paypal'
    UNION ALL SELECT 'reply', 'en', 11, 'Useful links', 'contacts', 'any',
           'Useful:
— the site and examples of work: {site}
— your request number: {id}
— the status and the whole conversation are in your personal account: {account}
— my time zone is Kyiv (UTC+2, UTC+3 in summer).',
           'contact, phone, email, reach you, get in touch, working hours, schedule, available, weekend, time zone, account, status'
    UNION ALL SELECT 'reply', 'en', 12, 'Support after the work', 'support', 'any',
           'I don''t disappear once the work is done: I can keep the system on support — monitoring, updates, help if something breaks. We''ll agree on the format and the cost of support separately.',
           'support, maintenance, after launch, after delivery, warranty, guarantee, if it breaks, monitoring, on-call'
    UNION ALL SELECT 'reply', 'en', 13, 'Confidentiality', 'nda', 'any',
           'I''m ready to sign an NDA. Please share the access in a secure way that suits you; once the work is done, it''s best to change it.',
           'nda, confidential, non-disclosure, access, credential, password, security, secret'
    UNION ALL SELECT 'reply', 'en', 14, 'A reminder', 'followup', 'waiting',
           '{name}, a quick reminder about your request {id}: I''m waiting for your answer to move on. If your plans have changed, just let me know and I''ll close the request.',
           ''
    UNION ALL SELECT 'reply', 'en', 15, 'Thank you for the order', 'thanks', 'done',
           'Thank you for the order, {name}! It was a pleasure to work with you. If you need anything else, write any time: regular clients get a discount.',
           'thank, thanks, great, works, accepted, appreciate'
) AS seed
WHERE NOT EXISTS (
    SELECT 1 FROM reply_templates t WHERE t.kind = seed.kind AND t.lang = seed.lang AND t.title = seed.title
);
