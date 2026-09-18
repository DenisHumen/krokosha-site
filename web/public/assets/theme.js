// Applies the colour theme the visitor chose earlier, before the first paint (no flash of the wrong theme).
// Without a saved choice the system theme applies through CSS. Loaded synchronously from <head>;
// kept as a separate tiny file so that the Content-Security-Policy needs no inline scripts.
(function () {
  try {
    var saved = localStorage.getItem('theme');
    if (saved === 'light' || saved === 'dark') {
      document.documentElement.dataset.theme = saved;
    }
  } catch {
    // Storage is blocked (private mode, strict settings): follow the system theme.
  }
})();
