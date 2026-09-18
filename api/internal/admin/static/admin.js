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
    [['time', item.time, ''], ['span', item.path, 'feed-path'], ['span', item.text, 'feed-text']].forEach(function (part) {
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
