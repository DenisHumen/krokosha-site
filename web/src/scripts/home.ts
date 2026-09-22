// Behaviour of the home page (design/pages/index.dc.html → Component, handoff/CHANGELOG.md): the
// scroll drives the hands, the headline, the dot of the rail, the curtain and the underline of the
// navigation; the hands and the easter eggs arrive after the page has loaded, so that the first
// screen — text — is as fast as it can be. Continuous values go straight to element.style (allowed
// by the Content-Security-Policy, unlike style attributes in markup).

interface EggsConfig {
  flags: { enabled: boolean; achievements?: boolean; [egg: string]: boolean | undefined };
  experience: number;
  texts: Record<string, unknown>;
}

interface EggEvent {
  count: number;
  total: number;
}

type EggsModule = typeof import('../../../design/components/eggs/eggs.js');

const clamp = (value: number, low: number, high: number) => Math.max(low, Math.min(high, value));

/** After `load`, when the browser has a moment: work that must not compete with the first paint. */
function afterLoad(work: () => void, delay = 0) {
  const start = () =>
    window.setTimeout(() => {
      if ('requestIdleCallback' in window) window.requestIdleCallback(work, { timeout: 2000 });
      else work();
    }, delay);
  if (document.readyState === 'complete') start();
  else window.addEventListener('load', start, { once: true });
}

export function initHome(): void {
  const hero = document.querySelector<HTMLElement>('[data-hero]');
  const hands = document.querySelector<HTMLElement>('[data-hands]');
  const heroText = document.querySelector<HTMLElement>('[data-hero-text]');
  const caption = document.querySelector<HTMLElement>('[data-link-caption]');
  const railDot = document.querySelector<HTMLElement>('[data-rail-dot]');
  const curtain = document.querySelector<HTMLElement>('[data-curtain]');
  const ring = document.querySelector<HTMLElement>('[data-curtain-ring]');
  const curtainHeading = document.querySelector<HTMLElement>('[data-curtain-heading]');
  const curtainText = document.querySelector<HTMLElement>('[data-curtain-text]');
  const navLinks = [...document.querySelectorAll<HTMLAnchorElement>('[data-nav]')];
  const sections = navLinks.flatMap((link) => {
    const section = document.getElementById(link.dataset['nav'] ?? '');
    return section ? [section] : [];
  });
  const phone = matchMedia('(max-width: 900px)');

  // --- the hands ------------------------------------------------------------------------------------

  // Across the screen on wide ones, from top and bottom on phones.
  const layout = () => hands?.setAttribute('layout', phone.matches ? 'v' : 'h');
  layout();
  phone.addEventListener('change', layout);
  if (hands) afterLoad(() => void import('./hands.ts'));

  // «link up» shows when the fingers meet.
  document.addEventListener('kro:link', () => caption?.classList.add('is-shown'));
  document.addEventListener('kro:unlink', () => caption?.classList.remove('is-shown'));

  // --- the scroll -----------------------------------------------------------------------------------

  let pending = false;
  function frame() {
    pending = false;
    const y = window.scrollY;
    const vh = window.innerHeight;

    if (hero) {
      // The hero is 260vh tall: 0 when it starts, 1 when its last screen leaves.
      const progress = clamp(y / Math.max(1, hero.offsetHeight - vh), 0, 1);
      hands?.setAttribute('progress', progress.toFixed(4));
      if (heroText) {
        const k = Math.min(1, progress / 0.55);
        heroText.style.transform = k ? `translateY(${-k * 80}px)` : '';
        heroText.style.opacity = k ? String(1 - k) : '';
        heroText.style.pointerEvents = k > 0.9 ? 'none' : '';
      }
    }

    if (railDot) {
      const scrollable = document.documentElement.scrollHeight - vh;
      railDot.style.top = `calc(${scrollable > 0 ? (y / scrollable) * 100 : 0}% - 3px)`;
    }

    if (curtain) {
      // −1…1 while the curtain crosses the screen: the heading, the text and the ring move at their own pace.
      const t = clamp(-curtain.getBoundingClientRect().top / vh, -1, 1);
      if (curtainHeading) curtainHeading.style.transform = `translateY(${t * -60}px)`;
      if (curtainText) curtainText.style.transform = `translateY(${t * -30}px)`;
      if (ring) ring.style.transform = `translateY(calc(-50% + ${t * 120}px)) rotate(${t * 20}deg)`;
    }

    // The section that has reached the upper part of the screen is the current one.
    let active = '';
    for (const section of sections) {
      if (section.getBoundingClientRect().top < vh * 0.45) active = section.id;
    }
    for (const link of navLinks) {
      if (link.dataset['nav'] === active) link.setAttribute('aria-current', 'location');
      else link.removeAttribute('aria-current');
    }
  }

  const onScroll = () => {
    if (pending) return;
    pending = true;
    requestAnimationFrame(frame);
  };
  window.addEventListener('scroll', onScroll, { passive: true });
  window.addEventListener('resize', onScroll);
  frame();

  // --- the phone menu (a popover): a chosen section closes it -----------------------------------------

  const menu = document.getElementById('site-menu');
  menu?.addEventListener('click', (event) => {
    if (event.target instanceof Element && event.target.closest('a')) menu.hidePopover();
  });

  // --- easter eggs (brief A6) -------------------------------------------------------------------------

  const config = readEggs();
  if (!config?.flags.enabled) return;
  const achievements = config.flags.achievements !== false;
  const captions = document.querySelectorAll<HTMLElement>('[data-eggs-caption]');
  const count = (found: number, total: number) => {
    if (!achievements) return;
    for (const node of captions) node.textContent = found > 0 ? `eggs ${found}/${total}` : '';
  };
  document.addEventListener('kro:egg', (event) => {
    const { count: found, total } = (event as CustomEvent<EggEvent>).detail;
    count(found, total);
  });

  let eggs: EggsModule | null = null;
  afterLoad(() => {
    void import('../../../design/components/eggs/eggs.js').then((module) => {
      eggs = module;
      count(module.foundList().length, module.TOTAL);
      module.initEggs({ flags: config.flags, texts: config.texts, experience: config.experience });
    });
  }, 800);

  // Five clicks on the hero avatar within a moment: it «reboots» like a server.
  const avatar = document.querySelector<HTMLElement>('[data-avatar-reboot]');
  let clicks = 0;
  let reset = 0;
  avatar?.addEventListener('click', () => {
    clicks += 1;
    window.clearTimeout(reset);
    reset = window.setTimeout(() => (clicks = 0), 1500);
    if (clicks >= 5) {
      clicks = 0;
      eggs?.rebootAvatar(avatar, config.texts);
    }
  });
}

function readEggs(): EggsConfig | null {
  const node = document.getElementById('kro-eggs');
  if (!node?.textContent) return null;
  try {
    return JSON.parse(node.textContent) as EggsConfig;
  } catch {
    return null;
  }
}
