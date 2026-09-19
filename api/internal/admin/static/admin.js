// Admin area: the live part of the overview. Everything else works without JavaScript.
//
// The server streams two kinds of events (Server-Sent Events, same origin, the session cookie):
//   active    {"count": 3}                                          «now on the site»
//   activity  {"time": "18:04:33", "visitor": "a1b2c3d4", "path": "/uk/", "text": "клик: Telegram"}
// Lines are built with textContent only: whatever a visitor managed to put into a path or a
// target stays text.
(function () {
  'use strict';

  var board = document.querySelector('[data-live]');
  if (!board || !window.EventSource) return;

  var counter = document.getElementById('active-count');
  var feed = document.getElementById('feed');
  var state = document.getElementById('live-state');
  var MAX_LINES = 60;
  var failures = 0;

  function say(message) {
    if (!state) return;
    state.textContent = message;
    state.hidden = !message;
  }

  function line(item) {
    var row = document.createElement('li');
    row.className = 'is-new';
    [
      ['time', item.time, ''],
      ['span', item.path, 'feed-path'],
      ['span', item.text, 'feed-text'],
    ].forEach(function (part) {
      var node = document.createElement(part[0]);
      node.textContent = part[1];
      if (part[2]) node.className = part[2];
      row.appendChild(node);
    });
    return row;
  }

  var source = new EventSource(board.getAttribute('data-live'));

  source.addEventListener('active', function (event) {
    failures = 0;
    say('');
    if (counter) counter.textContent = String(JSON.parse(event.data).count);
  });

  source.addEventListener('activity', function (event) {
    if (!feed) return;
    var empty = document.getElementById('feed-empty');
    if (empty) empty.remove();
    feed.insertBefore(line(JSON.parse(event.data)), feed.firstChild);
    while (feed.children.length > MAX_LINES) feed.removeChild(feed.lastChild);
  });

  // The server says goodbye when the session is over: reconnecting would only collect 401s.
  source.addEventListener('bye', function () {
    source.close();
    say('Сеанс завершён — войдите снова, чтобы видеть живые данные.');
  });

  source.onerror = function () {
    failures += 1;
    if (source.readyState === EventSource.CLOSED || failures > 5) {
      source.close();
      say('Живое обновление остановлено. Обновите страницу.');
    } else {
      say('Переподключение…');
    }
  };
})();

// The board of requests: a card dragged to another column changes its status. The server decides
// whether that is allowed (and who took a request first); if it says no, the card goes back and
// the reason is shown. Without this script the same buttons are on the card's own page.
(function () {
  'use strict';

  var board = document.querySelector('[data-board]');
  if (!board || !window.fetch) return;

  var state = document.getElementById('board-state');
  var dragged = null;

  function say(message) {
    if (!state) return;
    state.textContent = message;
    state.hidden = !message;
  }

  function recount() {
    Array.prototype.forEach.call(board.querySelectorAll('[data-status]'), function (column) {
      var count = column.querySelector('[data-count]');
      if (count) count.textContent = column.querySelectorAll('[data-lead]').length;
    });
  }

  function clearTargets(except) {
    Array.prototype.forEach.call(board.querySelectorAll('.is-target'), function (column) {
      if (column !== except) column.classList.remove('is-target');
    });
  }

  board.addEventListener('dragstart', function (event) {
    var card = event.target.closest && event.target.closest('[data-lead]');
    if (!card) return;
    dragged = card;
    card.classList.add('is-dragged');
    event.dataTransfer.effectAllowed = 'move';
    event.dataTransfer.setData('text/plain', card.getAttribute('data-lead'));
  });

  board.addEventListener('dragend', function () {
    if (dragged) dragged.classList.remove('is-dragged');
    dragged = null;
    clearTargets(null);
  });

  board.addEventListener('dragover', function (event) {
    var column = event.target.closest && event.target.closest('[data-status]');
    if (!column || !dragged) return;
    event.preventDefault();
    clearTargets(column);
    column.classList.add('is-target');
  });

  board.addEventListener('drop', function (event) {
    var column = event.target.closest && event.target.closest('[data-status]');
    var card = dragged;
    if (!column || !card) return;
    event.preventDefault();
    var from = card.closest('[data-status]');
    if (from === column) return;

    var body = new URLSearchParams();
    body.set('csrf', board.getAttribute('data-csrf'));
    body.set('status', column.getAttribute('data-status'));
    column.querySelector('.board-cards').appendChild(card);
    recount();
    say('');

    function back(message) {
      from.querySelector('.board-cards').appendChild(card);
      recount();
      say(message);
    }

    fetch(board.getAttribute('data-action') + card.getAttribute('data-lead') + '/status', {
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/x-www-form-urlencoded' },
      body: body.toString(),
    })
      .then(function (response) {
        return response
          .json()
          .catch(function () {
            return {};
          })
          .then(function (result) {
            if (!response.ok || !result.ok)
              back(result.error || 'Не получилось изменить статус. Обновите страницу.');
          });
      })
      .catch(function () {
        back('Нет связи с сервером. Обновите страницу.');
      });
  });
})();
