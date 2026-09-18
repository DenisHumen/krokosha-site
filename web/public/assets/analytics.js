/*
 * Visit statistics of this site: first-party, no cookies, nothing stored in your browser.
 * Served as written — not minified — so anyone can read it. Both sides are open source:
 * https://github.com/DenisHumen/krokosha-site (web/public/assets, api/internal/analytics)
 *
 * Sent to /api/e on this same site:
 *   - page path and language, the HOST you came from (never the full address);
 *   - utm_* tags, and whether an ad click id was present (not its value);
 *   - sections reached, scroll depth (25/50/75/100 %), time the page was visible;
 *   - clicks on contact buttons and outbound links.
 * The server adds a rough device / browser / OS type and keeps your IP only truncated.
 *
 * Never collected: keystrokes, form input, mouse movements, recordings, fingerprints.
 * With «Do Not Track» or «Global Privacy Control» on, nothing is sent at all.
 */
(function () {
  'use strict';

  var nav = navigator;
  var doc = document;
  if (nav.doNotTrack === '1' || nav.globalPrivacyControl) return;
  if (nav.webdriver || !window.crypto) return; // automated or very old browsers

  var ENDPOINT = '/api/e';
  var started = Date.now();
  var queue = [];
  var timer = 0;

  // A random id of this page view. It lives in memory only and dies with the page.
  var bytes = crypto.getRandomValues(new Uint8Array(8));
  var pageId = '';
  for (var i = 0; i < bytes.length; i++) pageId += ('0' + bytes[i].toString(16)).slice(-2);

  var params = new URLSearchParams(location.search);
  function utm(name) {
    return params.get('utm_' + name) || '';
  }
  var referrer = '';
  try {
    referrer = doc.referrer ? new URL(doc.referrer).hostname : '';
  } catch {
    /* malformed referrer: treat as none */
  }

  function push(type, target, value) {
    var event = { t: type, o: Date.now() - started };
    // Identifiers only: the server refuses the whole batch over one stray character.
    if (target)
      event.x = ('' + target)
        .replace(/[^A-Za-z0-9_.:-]/g, '-')
        .replace(/^[^A-Za-z0-9]/, 'x')
        .slice(0, 100);
    if (value !== undefined) event.v = value;
    queue.push(event);
    if (queue.length >= 30) flush();
    else if (!timer) timer = setTimeout(flush, 10000);
  }

  function flush() {
    clearTimeout(timer);
    timer = 0;
    if (!queue.length) return;
    var body = JSON.stringify({
      v: 1,
      id: pageId,
      p: location.pathname,
      l: doc.documentElement.lang.slice(0, 2),
      r: referrer,
      u: {
        s: utm('source'),
        m: utm('medium'),
        c: utm('campaign'),
        t: utm('term'),
        n: utm('content'),
      },
      ad: params.has('gclid') ? 'g' : params.has('fbclid') ? 'f' : params.has('msclkid') ? 'm' : '',
      e: queue,
    });
    queue = [];
    // sendBeacon survives the page being closed; fetch is the fallback.
    var sent =
      nav.sendBeacon && nav.sendBeacon(ENDPOINT, new Blob([body], { type: 'application/json' }));
    if (!sent && window.fetch) {
      fetch(ENDPOINT, {
        method: 'POST',
        body: body,
        keepalive: true,
        headers: { 'Content-Type': 'application/json' },
      }).catch(function () {});
    }
  }

  // Page view. Sent after a second, so that even a very short visit is counted.
  push('pageview');
  clearTimeout(timer);
  timer = setTimeout(flush, 1000);

  // Scroll depth: 25 / 50 / 75 / 100 %, each reported once.
  var reached = 0;
  var scrollPending = false;
  function measureScroll() {
    scrollPending = false;
    var root = doc.documentElement;
    var percent = ((window.scrollY + window.innerHeight) / Math.max(root.scrollHeight, 1)) * 100;
    [25, 50, 75, 100].forEach(function (mark) {
      if (percent >= mark - 1 && reached < mark) {
        reached = mark;
        push('scroll', '', mark);
      }
    });
  }
  window.addEventListener(
    'scroll',
    function () {
      if (!scrollPending) {
        scrollPending = true;
        requestAnimationFrame(measureScroll);
      }
    },
    { passive: true },
  );

  // Sections: which were seen and for how long. «On screen» = crossing the middle third of the
  // window (works for sections taller than the screen) or visible as a whole (a short footer).
  var seen = {};
  var flags = {};
  var onScreen = {};
  var visibleSince = {};
  function leaveSection(name) {
    if (!visibleSince[name]) return;
    var ms = Date.now() - visibleSince[name];
    visibleSince[name] = 0;
    if (ms >= 1000) push('section_time', name, ms);
  }
  function watch(flag, options) {
    var observer = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        var name = entry.target.getAttribute('data-section');
        var state = (flags[name] = flags[name] || {});
        state[flag] = flag === 'whole' ? entry.intersectionRatio > 0.98 : entry.isIntersecting;
        onScreen[name] = state.middle || state.whole;
        if (!onScreen[name]) return leaveSection(name);
        if (!seen[name]) {
          seen[name] = true;
          push('section', name);
        }
        visibleSince[name] = visibleSince[name] || Date.now();
      });
    }, options);
    doc.querySelectorAll('[data-section]').forEach(function (section) {
      observer.observe(section);
    });
  }
  if (window.IntersectionObserver) {
    watch('middle', { rootMargin: '-33% 0px -33% 0px' });
    watch('whole', { threshold: 0.99 });
  }

  // Clicks on elements marked data-track, and links that lead away from the site.
  doc.addEventListener(
    'click',
    function (event) {
      var tracked = event.target.closest && event.target.closest('[data-track]');
      if (tracked) push('click', tracked.getAttribute('data-track'));

      var link = event.target.closest && event.target.closest('a[href]');
      if (!link) return;
      var outbound = '';
      if (link.protocol === 'mailto:' || link.protocol === 'tel:')
        outbound = link.protocol.slice(0, -1);
      else if (link.hostname && link.hostname !== location.hostname) outbound = link.hostname;
      if (outbound) {
        push('outbound', outbound);
        flush(); // the page may be gone in a moment
      }
    },
    true,
  );

  // Easter eggs announce themselves with a «krokosha:egg» event, detail = the egg's id.
  doc.addEventListener('krokosha:egg', function (event) {
    if (typeof event.detail === 'string') push('egg', event.detail);
  });

  // Time on page counts only while the tab is visible.
  var visibleMs = 0;
  var shownAt = doc.visibilityState === 'visible' ? started : 0;
  function pause() {
    if (shownAt) {
      visibleMs += Date.now() - shownAt;
      shownAt = 0;
    }
    Object.keys(visibleSince).forEach(leaveSection);
    push('time', '', visibleMs);
    flush();
  }
  doc.addEventListener('visibilitychange', function () {
    if (doc.visibilityState === 'hidden') return pause();
    shownAt = Date.now();
    Object.keys(onScreen).forEach(function (name) {
      if (onScreen[name]) visibleSince[name] = Date.now();
    });
  });
  window.addEventListener('pagehide', pause);
})();
