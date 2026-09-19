/*
 * The contact form, improved for browsers with JavaScript. Without this file the form still works:
 * the browser checks the fields and posts them to /api/leads, which answers with a page.
 *
 * What this adds:
 *   - the contact field follows the chosen method (email / Telegram / phone);
 *   - errors next to the fields, in the language of the page;
 *   - a proof of work against spam — the browser solves a small puzzle from /api/leads/challenge
 *     while the form is being filled in (no cookies, no third parties, nothing to track);
 *   - sending without leaving the page.
 */
(function () {
  'use strict';

  var form = document.querySelector('form[data-form]');
  if (!form || !window.fetch || !window.FormData) return;

  var texts = JSON.parse(form.getAttribute('data-texts') || '{}');
  var errors = texts.errors || {};
  var root = form.parentNode;
  var contact = form.elements.contact_value;
  var button = form.querySelector('button[type="submit"]');
  var buttonText = button.textContent;
  // The browser's own checks (and their messages in the browser's language, not the page's) stay
  // for visitors without JavaScript; from here on this script does the checking.
  form.noValidate = true;

  // --- the contact field ------------------------------------------------------------------------

  var kinds = {
    email: { type: 'email', inputmode: 'email', autocomplete: 'email' },
    telegram: { type: 'text', inputmode: 'text', autocomplete: 'off' },
    phone: { type: 'tel', inputmode: 'tel', autocomplete: 'tel' },
  };

  function method() {
    var checked = form.querySelector('input[name="contact_method"]:checked');
    return checked ? checked.value : 'email';
  }

  function applyMethod() {
    var kind = kinds[method()] || kinds.email;
    contact.type = kind.type;
    contact.setAttribute('inputmode', kind.inputmode);
    contact.setAttribute('autocomplete', kind.autocomplete);
    contact.placeholder = (texts.placeholders || {})[method()] || '';
    show('contact_value', '');
  }

  Array.prototype.forEach.call(
    form.querySelectorAll('input[name="contact_method"]'),
    function (radio) {
      radio.addEventListener('change', applyMethod);
    },
  );
  applyMethod();

  // --- errors -----------------------------------------------------------------------------------

  function show(field, code) {
    var slot = form.querySelector('[data-error-for="' + field + '"]');
    var input = form.elements[field];
    if (slot) slot.textContent = code ? errors[code] || errors.required || '' : '';
    if (input && input.setAttribute) {
      if (code) input.setAttribute('aria-invalid', 'true');
      else input.removeAttribute('aria-invalid');
    }
  }

  function check() {
    var value = contact.value.trim();
    var found = {};
    if (!form.elements.name.value.trim()) found.name = 'required';
    if (!value) found.contact_value = 'required';
    else if (method() === 'email' && !/^[^\s@<>()[\],;:"]+@[^\s@]+\.[^\s@.]+$/.test(value))
      found.contact_value = 'invalid_email';
    else if (
      method() === 'telegram' &&
      !/^(https?:\/\/)?(t\.me\/|telegram\.me\/)?@?[A-Za-z][A-Za-z0-9_]{4,31}\/?$/.test(value)
    )
      found.contact_value = 'invalid_telegram';
    else if (method() === 'phone' && !/^\+?[0-9]{7,15}$/.test(value.replace(/[\s()./-]/g, '')))
      found.contact_value = 'invalid_phone';
    if (!form.elements.direction.value) found.direction = 'required';
    var length = Array.from(form.elements.description.value.trim()).length;
    if (!length) found.description = 'required';
    else if (length < 20 || length > 4000) found.description = 'description_length';
    if (!form.elements.consent.checked) found.consent = 'consent_required';
    return found;
  }

  function showAll(found) {
    ['name', 'contact_value', 'direction', 'description', 'consent'].forEach(function (field) {
      show(field, found[field]);
    });
    var first = Object.keys(found)[0];
    if (first && form.elements[first] && form.elements[first].focus) form.elements[first].focus();
  }

  form.addEventListener('input', function (event) {
    if (event.target.name) show(event.target.name, '');
  });

  // --- proof of work ----------------------------------------------------------------------------
  // SHA-256(salt + N) must equal the challenge; N is below maxnumber. Compatible with ALTCHA.

  var proof = null; // a promise of the solved payload, started when the visitor touches the form

  function hex(buffer) {
    return Array.prototype.map
      .call(new Uint8Array(buffer), function (byte) {
        return (byte < 16 ? '0' : '') + byte.toString(16);
      })
      .join('');
  }

  async function solve(challenge) {
    var encoder = new TextEncoder();
    for (var number = 0; number <= challenge.maxnumber; number++) {
      var digest = await crypto.subtle.digest('SHA-256', encoder.encode(challenge.salt + number));
      if (hex(digest) === challenge.challenge) {
        return btoa(
          JSON.stringify({
            algorithm: challenge.algorithm,
            challenge: challenge.challenge,
            number: number,
            salt: challenge.salt,
            signature: challenge.signature,
          }),
        );
      }
      // A breath every few hundred tries, so that typing stays smooth while the puzzle is solved.
      if (number % 500 === 499) await new Promise((resolve) => setTimeout(resolve, 0));
    }
    return '';
  }

  function startProof() {
    if (proof || !window.crypto || !crypto.subtle || !window.TextEncoder) return;
    proof = fetch('/api/leads/challenge', { headers: { Accept: 'application/json' } })
      .then(function (response) {
        return response.ok ? response.json() : null;
      })
      .then(function (challenge) {
        return challenge && challenge.challenge ? solve(challenge) : '';
      })
      .catch(function () {
        return ''; // the form is sent without a proof; the server decides what that means
      });
  }

  form.addEventListener('focusin', startProof);
  form.addEventListener('pointerdown', startProof);

  // --- sending ----------------------------------------------------------------------------------

  function message(id) {
    Array.prototype.forEach.call(root.querySelectorAll('.form-message'), function (node) {
      node.classList.toggle('is-shown', node.id === id);
    });
    var shown = id && document.getElementById(id);
    if (shown && shown.focus) shown.focus();
  }

  function succeed(result) {
    var block = document.getElementById('form-success');
    block.querySelector('[data-field="title"]').textContent = (texts.success || '{id}').replace(
      '{id}',
      '#' + result.id,
    );
    var telegram = block.querySelector('[data-field="telegram"]');
    if (result.telegram_url && /^https:\/\/t\.me\//.test(result.telegram_url)) {
      telegram.querySelector('a').href = result.telegram_url;
      telegram.hidden = false;
    }
    form.hidden = true;
    message('form-success');
    document.dispatchEvent(new CustomEvent('krokosha:lead', { detail: { id: result.id } }));
  }

  form.addEventListener('submit', function (event) {
    event.preventDefault();
    var found = check();
    showAll(found);
    if (Object.keys(found).length) return;

    message('');
    button.disabled = true;
    button.textContent = texts.sending || buttonText;
    startProof();

    (proof || Promise.resolve(''))
      .then(function (payload) {
        form.elements.altcha.value = payload || '';
        return fetch(form.action, {
          method: 'POST',
          headers: { Accept: 'application/json' },
          body: new FormData(form),
        });
      })
      .then(function (response) {
        return response
          .json()
          .catch(function () {
            return {};
          })
          .then(function (result) {
            if (response.status === 201 && result.ok) return succeed(result);
            if (response.status === 422 && result.errors) {
              showAll(result.errors);
              return message('form-error-invalid');
            }
            message(response.status === 429 ? 'form-error-rate' : 'form-error-server');
          });
      })
      .catch(function () {
        message('form-error-server');
      })
      .then(function () {
        proof = null; // one proof, one request: the next attempt solves a new puzzle
        button.disabled = false;
        button.textContent = buttonText;
      });
  });
})();
