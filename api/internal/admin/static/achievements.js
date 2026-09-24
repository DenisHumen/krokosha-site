// «Ачивки» → the bench: every button shows the site's own banner (achievement.js — a copy of
// design/components/eggs/achievement.js that a test keeps the same) with the sound of its kind.
import { unlock } from './achievement.js';

const withSound = document.getElementById('toast-sound');

for (const button of document.querySelectorAll('[data-toast]')) {
  button.addEventListener('click', () => {
    const given = JSON.parse(button.getAttribute('data-toast') || '[]');
    for (const toast of Array.isArray(given) ? given : [given]) {
      unlock({ ...toast, silent: withSound ? !withSound.checked : false });
    }
  });
}
