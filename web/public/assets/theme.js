// Runs before the first paint, synchronously from <head>, and does two things:
//   - applies the colour theme the visitor chose earlier (no flash of the wrong theme); without a
//     saved choice the system theme applies through CSS (design/tokens.css);
//   - marks the page as scripted (<html class="js">): parts that need a script to work — the skills
//     search, the collapsed racks — take their interactive form only then, and stay plain otherwise.
// A separate tiny file, so that the Content-Security-Policy needs no inline scripts.
(function () {
  document.documentElement.classList.add('js');
  try {
    var saved = localStorage.getItem('theme');
    if (saved === 'light' || saved === 'dark') {
      document.documentElement.dataset.theme = saved;
    }
  } catch {
    // Storage is blocked (private mode, strict settings): follow the system theme.
  }
})();
