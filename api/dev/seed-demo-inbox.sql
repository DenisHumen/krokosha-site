-- Made-up letters for the «Входящие» screen of the local admin area (api/dev/run-local.sh).
-- Nothing here is used on the server. Safe to load again: the keys are fixed.

INSERT IGNORE INTO inbox_letters
    (received_at, message_key, outcome, uid, uid_validity, message_id, from_name, from_address, subject, excerpt, files, automatic, note)
VALUES
    (NOW(3) - INTERVAL 35 MINUTE, UNHEX(SHA2('demo-letter-1', 256)), 'unmatched', 101, 1700000000, 'demo-1@else.example',
     'Олена Шевченко', 'olena@else.example', 'Сеть в новом офисе',
     'Добрый день!\n\nВас рекомендовал Иван из «Кофейни на Подоле». Переезжаем в новый офис на 25 мест, нужна сеть с нуля: стойка, коммутаторы, Wi-Fi.\nПлан этажа прикладываю. Когда удобно созвониться?\n\n--\nОлена',
     1, 0, NULL),
    (NOW(3) - INTERVAL 3 HOUR, UNHEX(SHA2('demo-letter-2', 256)), 'unmatched', 102, 1700000000, 'demo-2@company.example',
     'Иван Петров', 'ivan.p@gmail.example', 'Re: Заявка #K-0004 принята — krokosha.xyz',
     'Забыл приложить спецификацию, отправляю с личной почты.',
     2, 0, 'заявка K-0004 удалена или обезличена'),
    (NOW(3) - INTERVAL 1 DAY, UNHEX(SHA2('demo-letter-3', 256)), 'unmatched', 103, 1700000000, 'demo-3@mailer.example',
     'Mail Delivery System', 'mailer-daemon@mx.partner.example', 'Undelivered Mail Returned to Sender',
     'This is the mail system at host mx.partner.example.\n\nI''m sorry to have to inform you that your message could not be delivered to one or more recipients.',
     0, 1, 'отчёт почтового сервера о доставке — к какой заявке, не сказано'),
    (NOW(3) - INTERVAL 2 DAY, UNHEX(SHA2('demo-letter-4', 256)), 'unmatched', 104, 1700000000, 'demo-4@promo.example',
     '<b>SEO Rank First</b>', 'sales@rank-first.example', 'Ваш сайт теряет клиентов!!! <script>alert(1)</script>',
     'Здравствуйте! Мы провели аудит вашего сайта и нашли 47 критических ошибок. Первое место в Google за 14 дней — гарантия!\nhttps://rank-first.example/offer?id=123',
     0, 1, NULL);
