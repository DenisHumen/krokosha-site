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

// Quick answers (lead.html): a template goes into the answer without a reload — its text is added
// to what is there, its files come along as checked boxes, and it is sent as one of the answer's
// templates. Without scripts every template is a link that puts its text into the form.
(function () {
  'use strict';
  var composer = document.querySelector('[data-composer]');
  if (!composer) return;
  var form = composer.querySelector('[data-reply]');
  var source = document.getElementById('templates-data');
  if (!form || !source) return;
  var templates;
  try {
    templates = JSON.parse(source.textContent || '{}');
  } catch (error) {
    return;
  }
  var text = form.querySelector('textarea[name="text"]');
  var chosen = form.querySelector('[data-chosen]');
  var list = form.querySelector('[data-attach-list]');
  var base = (document.querySelector('[data-reply]').getAttribute('action') || '').replace(/\/upload\/leads\/\d+\/reply$/, '');

  function has(name, value) {
    return form.querySelector('input[name="' + name + '"][value="' + value + '"]') !== null;
  }

  function addFile(file) {
    if (!list || has('media', file.id)) return;
    var item = document.createElement('li');
    item.className = 'attach-item';
    var label = document.createElement('label');
    label.className = 'attach-check';
    var box = document.createElement('input');
    box.type = 'checkbox';
    box.name = 'media';
    box.value = String(file.id);
    box.checked = true;
    label.appendChild(box);
    var preview;
    if (file.kind === 'jpg' || file.kind === 'png') {
      preview = document.createElement('img');
      preview.className = 'attach-thumb';
      preview.alt = '';
      preview.loading = 'lazy';
      preview.src = base + '/templates/media/' + file.id;
    } else {
      preview = document.createElement('span');
      preview.className = 'attach-icon';
      preview.setAttribute('aria-hidden', 'true');
      preview.textContent = file.kind === 'mp4' ? '▶' : '≡';
    }
    label.appendChild(preview);
    var name = document.createElement('span');
    name.className = 'attach-name';
    name.textContent = file.name;
    label.appendChild(name);
    var size = document.createElement('span');
    size.className = 'attach-size';
    size.textContent = file.size;
    label.appendChild(size);
    item.appendChild(label);
    list.appendChild(item);
  }

  composer.addEventListener('click', function (event) {
    var link = event.target.closest('[data-template]');
    if (!link || event.metaKey || event.ctrlKey || event.shiftKey) return;
    var id = link.getAttribute('data-template');
    var template = templates[id];
    if (!template) return;
    event.preventDefault();
    if (!has('template', id)) {
      var current = text.value.replace(/\s+$/, '');
      text.value = current ? current + '\n\n' + template.body : template.body;
      var input = document.createElement('input');
      input.type = 'hidden';
      input.name = 'template';
      input.value = id;
      chosen.appendChild(input);
      (template.media || []).forEach(addFile);
    }
    composer.querySelectorAll('[data-template="' + id + '"]').forEach(function (other) {
      other.setAttribute('aria-current', 'true');
    });
    text.focus();
    text.setSelectionRange(text.value.length, text.value.length);
  });

  // The search over all the templates: by title and by text.
  var filter = composer.querySelector('[data-template-filter]');
  var all = composer.querySelector('[data-templates]');
  if (filter && all) {
    filter.hidden = false;
    filter.addEventListener('input', function () {
      var query = filter.value.trim().toLowerCase();
      all.querySelectorAll('[data-group]').forEach(function (group) {
        var shown = 0;
        group.querySelectorAll('[data-template]').forEach(function (chip) {
          var template = templates[chip.getAttribute('data-template')] || { body: '' };
          var match = !query || (chip.textContent + '\n' + template.body).toLowerCase().indexOf(query) >= 0;
          chip.hidden = !match;
          if (match) shown++;
        });
        group.hidden = shown === 0;
      });
    });
  }
})();

// Forms with files (lead.html, template.html): the limits are checked as soon as files are chosen,
// so that a video too big is named at once — not after a long upload, by a bare page of nginx. The
// server checks them again, by what is inside the files.
(function () {
  'use strict';
  var MB = 1024 * 1024;
  var limits = { photo: 10, video: 50, document: 20 };
  var names = { photo: 'фото', video: 'видео', document: 'документа' };
  var most = 10;
  var total = 110;

  function kindOf(file) {
    var name = file.name.toLowerCase();
    if (/\.(jpe?g|png)$/.test(name) || file.type === 'image/jpeg' || file.type === 'image/png') return 'photo';
    if (/\.mp4$/.test(name) || file.type === 'video/mp4') return 'video';
    return 'document';
  }

  function megabytes(bytes) {
    return String(Math.round((bytes / MB) * 10) / 10).replace('.', ',');
  }

  document.querySelectorAll('input[type="file"][data-upload]').forEach(function (input) {
    var form = input.form;
    var note = document.createElement('p');
    note.className = 'upload-problem';
    note.setAttribute('role', 'alert');
    note.hidden = true;
    (input.closest('label') || input).insertAdjacentElement('afterend', note);

    function check() {
      var problem = '';
      var sum = 0;
      var count = input.files.length + (Number(input.getAttribute('data-upload')) || 0);
      if (form) count += form.querySelectorAll('input[name="media"]:checked').length;
      for (var i = 0; i < input.files.length && !problem; i++) {
        var file = input.files[i];
        var kind = kindOf(file);
        sum += file.size;
        if (file.size > limits[kind] * MB) {
          problem = 'Файл «' + file.name + '» — ' + megabytes(file.size) + ' МБ, а для ' + names[kind] + ' предел ' + limits[kind] + ' МБ.';
        }
      }
      if (!problem && count > most) {
        problem = 'Файлов получится ' + count + ', а можно не больше ' + most + ': столько Telegram показывает одним альбомом.';
      }
      if (!problem && sum > total * MB) {
        problem = 'Файлы вместе — ' + megabytes(sum) + ' МБ, за раз можно до ' + total + ' МБ: разделите их на два ответа.';
      }
      input.setCustomValidity(problem);
      note.textContent = problem;
      note.hidden = !problem;
    }

    input.addEventListener('change', check);
    if (form) {
      form.addEventListener('change', function (event) {
        if (event.target.name === 'media') check();
      });
    }
  });
})();

// The messenger of «Заявки» (leads.html): Enter sends and Shift+Enter starts a new line, the field
// grows with the text, «Ответ / Заметка» changes the hint, the picker of channels says where the
// answer goes, the names of chosen files are shown, and a pulse asks every little while whether a
// client wrote: the open conversation reloads by itself when nothing is being written in it.
(function () {
  'use strict';
  var root = document.querySelector('[data-messenger]');
  if (!root) return;

  var form = root.querySelector('[data-reply]');
  var text = form && form.querySelector('textarea[data-grow]');
  var files = form && form.querySelector('input[type="file"][name="files"]');

  function grow() {
    if (!text) return;
    text.style.height = 'auto';
    var height = Math.min(text.scrollHeight + 2, 160);
    text.style.height = Math.max(height, 34) + 'px';
    text.style.overflowY = text.scrollHeight > 158 ? 'auto' : 'hidden';
  }

  if (text) {
    var hint = text.getAttribute('placeholder');
    text.addEventListener('input', grow);
    text.addEventListener('keydown', function (event) {
      if (event.key !== 'Enter' || event.shiftKey || event.isComposing || event.keyCode === 229) return;
      event.preventDefault();
      if (!text.value.trim()) return;
      if (form.requestSubmit) form.requestSubmit();
      else form.submit();
    });
    var button = form.querySelector('.composer-send');
    form.addEventListener('submit', function () {
      // One answer per press: a second Enter while the page is on its way would send it twice.
      window.setTimeout(function () {
        if (button) button.disabled = true;
      }, 0);
    });
    // Back to this page from the history: the button works again.
    window.addEventListener('pageshow', function () {
      if (button) button.disabled = false;
    });
    form.addEventListener('change', function (event) {
      if (event.target.name !== 'mode') return;
      var note = event.target.value === 'note';
      text.setAttribute('placeholder', note ? text.getAttribute('data-note-placeholder') : hint);
      text.focus();
    });
    grow();
    // Whatever is typed goes to the end of what is there: the field opens at its end.
    if (text.value) text.setSelectionRange(text.value.length, text.value.length);
  }

  // The templates put their text in: the field grows, the list of all of them closes.
  root.addEventListener('click', function (event) {
    var chip = event.target.closest && event.target.closest('[data-template]');
    if (!chip) return;
    window.setTimeout(function () {
      grow();
      var all = chip.closest('details');
      if (all) all.open = false;
    }, 0);
  });

  // Where the answer goes: the summary follows the boxes.
  var via = root.querySelector('[data-via-text]');
  if (via && form) {
    var account = / · кабинет$/.test(via.textContent);
    form.addEventListener('change', function (event) {
      if (event.target.name !== 'channel') return;
      var names = [];
      form.querySelectorAll('input[name="channel"]').forEach(function (box) {
        if (box.checked) names.push(box.getAttribute('data-via-name'));
      });
      if (account) names.push('кабинет');
      via.textContent = names.length ? names.join(' · ') : 'никуда — отметьте канал';
    });
  }

  // The files chosen by hand, by name.
  var names = form && form.querySelector('[data-file-names]');
  if (files && names) {
    files.addEventListener('change', function () {
      var list = Array.prototype.map.call(files.files, function (file) {
        return file.name;
      });
      names.textContent = list.length ? 'Файлы: ' + list.join(', ') : '';
      names.hidden = !list.length;
    });
  }

  // What just happened fades away (CSS) and then leaves.
  var toast = document.querySelector('.full .content > .notice-ok');
  if (toast) {
    window.setTimeout(function () {
      toast.remove();
    }, 5200);
  }

  // The pulse. It does not keep the session alive (the server checks it without extending it).
  var pulse = root.getAttribute('data-pulse');
  var last = Number(root.getAttribute('data-last')) || 0;
  var lead = Number(root.getAttribute('data-lead')) || 0;
  var leadLast = Number(root.getAttribute('data-lead-last')) || 0;
  var news = root.querySelector('[data-news]');
  var title = document.title;
  if (!pulse || !window.fetch) return;

  function busy() {
    if (!form) return false;
    if (text && (text.value.trim() || document.activeElement === text)) return true;
    if (files && files.files.length) return true;
    return root.querySelector('details[open]') !== null;
  }

  var timer = 0;
  var failures = 0;
  function check() {
    if (document.hidden) return;
    fetch(pulse + '?lead=' + lead, { headers: { Accept: 'application/json' }, credentials: 'same-origin', cache: 'no-store' })
      .then(function (response) {
        if (!response.ok) throw new Error(String(response.status));
        return response.json();
      })
      .then(function (state) {
        failures = 0;
        document.title = state.unread > 0 ? '(' + state.unread + ') ' + title : title;
        if (lead && state.lead_last > leadLast) {
          if (!busy()) {
            // The address of the conversation itself: a page drawn after a form must not be sent again.
            window.location.replace(root.getAttribute('data-here') || window.location.pathname);
            return;
          }
          if (news) {
            news.textContent = 'Клиент написал в этот разговор — обновить';
            news.hidden = false;
          }
        } else if (state.last > last && news) {
          news.hidden = false;
        }
      })
      .catch(function () {
        // Signed out, or no connection: after a few tries the pulse stops; a reload restarts it.
        failures += 1;
        if (failures > 3) window.clearInterval(timer);
      });
  }
  timer = window.setInterval(check, 15000);
  document.addEventListener('visibilitychange', function () {
    if (!document.hidden) check();
  });
})();
