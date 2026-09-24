// Admin area: what scripts add. Every page works without them; they only make it quicker.
//
//   · the live part of the overview (Server-Sent Events): «now on the site» and the activity feed;
//   · the board of requests: drag a card to another column;
//   · menus close on a click elsewhere and on Escape;
//   · the background of glyphs can be switched off (remembered in this browser);
//   · Ctrl K (⌘K): jump to a section or to a request by its number.
//
// Text that came from visitors is only ever put in with textContent.
(function () {
  'use strict';

  // --- menus: <details> that close when the mouse or the focus goes elsewhere -----------------
  var menus = document.querySelectorAll('details[data-menu], details.period-custom');
  function closeMenus(except) {
    Array.prototype.forEach.call(menus, function (menu) {
      if (menu !== except && menu.open) menu.open = false;
    });
  }
  document.addEventListener('click', function (event) {
    var inside = event.target.closest && event.target.closest('details[data-menu], details.period-custom');
    closeMenus(inside);
  });
  document.addEventListener('keydown', function (event) {
    if (event.key === 'Escape') closeMenus(null);
  });

  // --- the sign-in code: digits only, so that a pasted «123 456» fits the six cells --------------
  var code = document.querySelector('.gate-code');
  if (code) {
    code.addEventListener('input', function () {
      var digits = code.value.replace(/\D+/g, '').slice(0, 6);
      if (digits !== code.value) code.value = digits;
    });
  }

  // --- the background of glyphs ------------------------------------------------------------------
  var BACKDROP_KEY = 'krokosha:admin-backdrop';
  var backdrop = document.querySelector('[data-backdrop]');
  var toggle = document.querySelector('[data-backdrop-toggle]');
  function stored() {
    try {
      return localStorage.getItem(BACKDROP_KEY);
    } catch (error) {
      return null;
    }
  }
  function applyBackdrop(off) {
    if (backdrop) backdrop.hidden = off;
    var state = document.querySelector('[data-backdrop-state]');
    if (state) state.textContent = off ? 'выкл' : 'вкл';
  }
  if (backdrop) {
    applyBackdrop(stored() === 'off');
    if (toggle) {
      toggle.hidden = false;
      toggle.addEventListener('click', function () {
        var off = !backdrop.hidden;
        try {
          if (off) localStorage.setItem(BACKDROP_KEY, 'off');
          else localStorage.removeItem(BACKDROP_KEY);
        } catch (error) {
          /* private mode: it holds until the page is left */
        }
        applyBackdrop(off);
      });
    }
  }

  // --- Ctrl K: jump anywhere ----------------------------------------------------------------------
  (function () {
    var rail = document.querySelectorAll('.rail-link');
    if (!rail.length || !window.HTMLDialogElement) return;
    var logo = document.querySelector('.rail-logo');
    var base = logo ? logo.getAttribute('href').replace(/\/$/, '') : '';
    var dialog = document.createElement('dialog');
    dialog.className = 'jump';
    var form = document.createElement('form');
    form.method = 'dialog';
    var input = document.createElement('input');
    input.className = 'input';
    input.type = 'search';
    input.placeholder = 'Раздел, номер заявки (42) или поиск по заявкам';
    input.setAttribute('aria-label', 'Куда перейти');
    var list = document.createElement('ul');
    list.className = 'jump-list';
    var hint = document.createElement('p');
    hint.className = 'jump-hint';
    hint.textContent = 'Enter — перейти · Esc — закрыть · Ctrl K — открыть отовсюду';
    form.appendChild(input);
    form.appendChild(list);
    form.appendChild(hint);
    dialog.appendChild(form);
    document.body.appendChild(dialog);

    var sections = Array.prototype.map.call(rail, function (link) {
      return { name: link.getAttribute('title'), href: link.getAttribute('href') };
    });

    function choices() {
      var text = input.value.trim();
      var out = [];
      var number = text.replace(/^#?k-?0*/i, '');
      if (/^\d{1,9}$/.test(number)) out.push({ name: 'Заявка K-' + ('0000' + number).slice(-Math.max(4, number.length)), href: base + '/leads/' + Number(number) });
      sections.forEach(function (section) {
        if (!text || section.name.toLowerCase().indexOf(text.toLowerCase()) !== -1) out.push(section);
      });
      if (text && !/^\d+$/.test(number)) out.push({ name: 'Искать в заявках: «' + text + '»', href: base + '/leads?q=' + encodeURIComponent(text) });
      return out;
    }
    function draw() {
      list.textContent = '';
      choices().forEach(function (choice, index) {
        var item = document.createElement('li');
        var link = document.createElement('a');
        link.href = choice.href;
        link.textContent = choice.name;
        if (index === 0) link.setAttribute('aria-current', 'true');
        item.appendChild(link);
        list.appendChild(item);
      });
    }
    function open() {
      closeMenus(null);
      input.value = '';
      draw();
      dialog.showModal();
      input.focus();
    }
    input.addEventListener('input', draw);
    form.addEventListener('submit', function (event) {
      event.preventDefault();
      var first = list.querySelector('a');
      if (first) window.location.href = first.href;
    });
    dialog.addEventListener('click', function (event) {
      if (event.target === dialog) dialog.close();
    });
    document.addEventListener('keydown', function (event) {
      if ((event.ctrlKey || event.metaKey) && (event.key === 'k' || event.key === 'K' || event.key === 'л' || event.key === 'Л')) {
        event.preventDefault();
        if (dialog.open) dialog.close();
        else open();
      }
    });
    var keys = document.querySelector('[data-keys]');
    if (keys) {
      keys.hidden = false;
      keys.addEventListener('click', open);
    }
  })();
})();

// --- the live part of the overview ---------------------------------------------------------------
//
// The server streams two kinds of events (same origin, the session cookie):
//   active    {"count": 3}                                          «now on the site»
//   activity  {"time": "18:04:33", "visitor": "a1b2c3d4", "path": "/uk/", "text": "клик: Telegram"}
(function () {
  'use strict';

  var board = document.querySelector('[data-live]');
  if (!board || !window.EventSource) return;

  var counter = document.getElementById('active-count');
  var feed = document.getElementById('feed');
  var state = document.getElementById('live-state');
  var MAX_LINES = 40;
  var failures = 0;

  function say(message) {
    if (!state) return;
    state.textContent = message;
    state.hidden = !message;
  }

  function line(item) {
    var row = document.createElement('li');
    row.className = 'is-new';
    var time = document.createElement('time');
    time.textContent = String(item.time || '').slice(0, 5);
    var text = document.createElement('span');
    text.className = 'feed-line';
    var path = document.createElement('span');
    path.className = 'feed-path';
    path.textContent = item.path;
    text.appendChild(path);
    text.appendChild(document.createTextNode(item.text));
    row.appendChild(time);
    row.appendChild(text);
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

// --- the board of requests: a card dragged to another column changes its status -------------------
//
// The server decides whether that is allowed (and who took a request first); if it says no, the
// card goes back and the reason is shown. Without this script the same buttons are on the card.
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
