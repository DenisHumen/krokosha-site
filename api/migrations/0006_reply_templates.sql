-- The ready-made answers and refusals of brief B10.4, in the three languages of the site. They are
-- a starting point: everything here is edited in the admin area («Шаблоны»), and this migration
-- adds them only to an empty table — it never brings back what the owner deleted.
-- {name} becomes the client's name, {id} the number of the request.

INSERT INTO reply_templates (kind, lang, position, title, body, updated_at)
SELECT seed.kind, seed.lang, seed.position, seed.title, seed.body, UTC_TIMESTAMP(3)
FROM (
    SELECT 'reply' AS kind, 'ru' AS lang, 1 AS position, 'Изучу и отвечу сегодня' AS title,
           'Здравствуйте, {name}!

Спасибо за заявку {id}. Изучу детали и отвечу до конца дня.' AS body
    UNION ALL SELECT 'reply', 'ru', 2, 'Нужны детали',
           'Здравствуйте, {name}!

Спасибо за заявку {id}. Чтобы оценить задачу, мне нужно уточнить несколько деталей:

—
—

Ответьте, пожалуйста, на это письмо — и я вернусь с оценкой.'
    UNION ALL SELECT 'reply', 'ru', 3, 'Давайте созвонимся',
           'Здравствуйте, {name}!

По заявке {id} проще всего поговорить голосом — 15–20 минут. Когда вам удобно созвониться? Напишите пару вариантов времени.'
    UNION ALL SELECT 'reply', 'ru', 4, 'Коммерческое предложение',
           'Здравствуйте, {name}!

По заявке {id} подготовил коммерческое предложение — оно во вложении. Если появятся вопросы или захочется что-то изменить, просто ответьте на это письмо.'

    UNION ALL SELECT 'reply', 'uk', 1, 'Вивчу й відповім сьогодні',
           'Вітаю, {name}!

Дякую за заявку {id}. Вивчу деталі й відповім до кінця дня.'
    UNION ALL SELECT 'reply', 'uk', 2, 'Потрібні деталі',
           'Вітаю, {name}!

Дякую за заявку {id}. Щоб оцінити задачу, мені потрібно уточнити кілька деталей:

—
—

Відповідайте, будь ласка, на цей лист — і я повернуся з оцінкою.'
    UNION ALL SELECT 'reply', 'uk', 3, 'Зідзвонімося',
           'Вітаю, {name}!

Щодо заявки {id} найпростіше поговорити голосом — 15–20 хвилин. Коли вам зручно зідзвонитися? Напишіть кілька варіантів часу.'
    UNION ALL SELECT 'reply', 'uk', 4, 'Комерційна пропозиція',
           'Вітаю, {name}!

За заявкою {id} підготував комерційну пропозицію — вона у вкладенні. Якщо виникнуть питання або захочеться щось змінити, просто відповідайте на цей лист.'

    UNION ALL SELECT 'reply', 'en', 1, 'I''ll look into it today',
           'Hello {name},

Thank you for your request {id}. I''ll look into the details and get back to you by the end of the day.'
    UNION ALL SELECT 'reply', 'en', 2, 'I need some details',
           'Hello {name},

Thank you for your request {id}. To estimate the work I need to clarify a few things:

—
—

Please reply to this email and I''ll come back with an estimate.'
    UNION ALL SELECT 'reply', 'en', 3, 'Let''s have a call',
           'Hello {name},

Regarding your request {id}: a short call, 15–20 minutes, would be the easiest way to discuss it. When would suit you? Please suggest a couple of time slots.'
    UNION ALL SELECT 'reply', 'en', 4, 'Proposal attached',
           'Hello {name},

I have prepared a proposal for your request {id} — please find it attached. If you have questions or would like to change something, just reply to this email.'

    UNION ALL SELECT 'reject', 'ru', 1, 'Не мой профиль',
           'Здравствуйте, {name}!

Спасибо за заявку {id}. К сожалению, эта задача вне моего профиля, и я не смогу сделать её так, как она того заслуживает. Желаю найти хорошего исполнителя!'
    UNION ALL SELECT 'reject', 'ru', 2, 'Нет свободного времени',
           'Здравствуйте, {name}!

Спасибо за заявку {id}. Сейчас у меня нет свободного времени, чтобы взяться за неё в разумные сроки. Если задача останется актуальной через месяц — напишите, буду рад вернуться к разговору.'
    UNION ALL SELECT 'reject', 'ru', 3, 'Бюджет не подходит',
           'Здравствуйте, {name}!

Спасибо за заявку {id}. В указанный бюджет эту задачу, к сожалению, не уложить без потери качества. Если бюджет можно пересмотреть — дайте знать, предложу варианты.'

    UNION ALL SELECT 'reject', 'uk', 1, 'Не мій профіль',
           'Вітаю, {name}!

Дякую за заявку {id}. На жаль, ця задача поза моїм профілем, і я не зможу зробити її так, як вона на те заслуговує. Бажаю знайти хорошого виконавця!'
    UNION ALL SELECT 'reject', 'uk', 2, 'Немає вільного часу',
           'Вітаю, {name}!

Дякую за заявку {id}. Зараз у мене немає вільного часу, щоб узятися за неї в розумні терміни. Якщо задача залишиться актуальною за місяць — напишіть, буду радий повернутися до розмови.'
    UNION ALL SELECT 'reject', 'uk', 3, 'Бюджет не підходить',
           'Вітаю, {name}!

Дякую за заявку {id}. У вказаний бюджет цю задачу, на жаль, не вкласти без втрати якості. Якщо бюджет можна переглянути — дайте знати, запропоную варіанти.'

    UNION ALL SELECT 'reject', 'en', 1, 'Not my field',
           'Hello {name},

Thank you for your request {id}. Unfortunately this task is outside my field, and I would not be able to do it the way it deserves. I hope you find the right specialist!'
    UNION ALL SELECT 'reject', 'en', 2, 'No free time',
           'Hello {name},

Thank you for your request {id}. At the moment I have no free time to take it on within a reasonable term. If the task is still open in a month, please write again — I''ll be glad to come back to it.'
    UNION ALL SELECT 'reject', 'en', 3, 'The budget does not fit',
           'Hello {name},

Thank you for your request {id}. Unfortunately the task cannot be done within the stated budget without losing quality. If the budget can be reconsidered, let me know and I''ll suggest options.'
) AS seed
WHERE NOT EXISTS (SELECT 1 FROM reply_templates);
