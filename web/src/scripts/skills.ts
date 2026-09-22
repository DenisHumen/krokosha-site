// The skills topology (components/Skills.astro, design: SKILLS): racks that open and close, the search
// over skills and notes, and the connector lines from the core to every rack — with packets running
// along the lines of the open ones. Lines are measured from the real layout, so they follow any font,
// language and width.

const SVG = 'http://www.w3.org/2000/svg';
/** Opening takes --dur-3 (640 ms); the lines to the units are drawn once the units are in place. */
const SETTLED_MS = 660;

interface Line {
  d: string;
  live: boolean;
}

interface Packet {
  d: string;
  duration: number;
  delay: number;
}

export function initSkills(): void {
  const topology = document.querySelector<HTMLElement>('[data-topology]');
  const svg = document.querySelector<SVGSVGElement>('[data-topology-lines]');
  const core = document.querySelector<HTMLElement>('[data-topology-core]');
  if (!topology || !svg || !core) return;

  const query = document.querySelector<HTMLInputElement>('[data-skills-query]');
  const empty = document.querySelector<HTMLElement>('[data-skills-empty]');
  const reduced = matchMedia('(prefers-reduced-motion: reduce)');
  const vertical = matchMedia('(max-width: 900px)');

  const racks = [...topology.querySelectorAll<HTMLElement>('[data-rack]')].flatMap((rack) => {
    const head = rack.querySelector<HTMLButtonElement>('button[aria-controls]');
    const body = head && document.getElementById(head.getAttribute('aria-controls') ?? '');
    if (!head || !body) return [];
    return [
      {
        id: rack.id,
        rack,
        head,
        body,
        units: [...rack.querySelectorAll<HTMLElement>('[data-level="group"]')],
        skills: [...rack.querySelectorAll<HTMLElement>('[data-level="skill"]')],
        hits: 0,
      },
    ];
  });
  if (racks.length === 0) return;

  const chosen = new Set<string>();
  let searching = false;

  const isOpen = (rack: (typeof racks)[number]) =>
    searching ? rack.hits > 0 : chosen.has(rack.id);

  function apply() {
    for (const rack of racks) {
      const open = isOpen(rack);
      rack.head.setAttribute('aria-expanded', String(open));
      rack.body.classList.toggle('is-open', open);
      // A closed rack is out of the tab order and out of the screen reader's way.
      rack.body.inert = !open;
      rack.rack.classList.toggle('is-dimmed', searching && rack.hits === 0);
    }
  }

  // --- lines ------------------------------------------------------------------------------------

  let drawn = '';

  function measure() {
    const box = topology!.getBoundingClientRect();
    const node = core!.getBoundingClientRect();
    const down = vertical.matches;
    const nx = node.left - box.left + node.width / 2;
    const ny = node.top - box.top + (down ? node.height : node.height / 2);
    const right = node.right - box.left;
    const lines: Line[] = [];
    const packets: Packet[] = [];

    racks.forEach((rack, i) => {
      const head = rack.head.getBoundingClientRect();
      const x = head.left - box.left;
      const y = head.top - box.top + head.height / 2;
      const open = isOpen(rack);
      const d = down
        ? `M${nx},${ny} C${nx},${ny + 40} ${x + 24},${y - 40} ${x + 24},${y - head.height / 2}`
        : `M${right},${ny} C${right + 60},${ny} ${x - 60},${y} ${x},${y}`;
      lines.push({ d, live: open });
      if (!open) return;
      packets.push({ d, duration: 1.6 + i * 0.1, delay: 0 });
      rack.units.forEach((unit, j) => {
        const box2 = unit.getBoundingClientRect();
        const ux = box2.left - box.left;
        const uy = box2.top - box.top + Math.min(box2.height / 2, 22);
        const sx = head.right - box.left;
        const unitLine = down
          ? `M${x + 24},${head.bottom - box.top} L${x + 24},${uy} L${ux},${uy}`
          : `M${sx},${y} C${sx + 40},${y} ${ux - 40},${uy} ${ux},${uy}`;
        lines.push({ d: unitLine, live: true });
        packets.push({ d: unitLine, duration: 1.2 + j * 0.15, delay: 0.4 + j * 0.2 });
      });
    });

    const key = JSON.stringify([lines, packets, reduced.matches]);
    if (key === drawn) return;
    drawn = key;
    draw(lines, packets);
  }

  function draw(lines: Line[], packets: Packet[]) {
    svg!.replaceChildren();
    for (const line of lines) {
      const path = document.createElementNS(SVG, 'path');
      path.setAttribute('d', line.d);
      if (line.live) path.setAttribute('class', 'live');
      svg!.append(path);
      // The line «draws itself»: a dash as long as the path, moved from hidden to shown (kro-dash).
      const length = Math.ceil(path.getTotalLength());
      path.setAttribute('stroke-dasharray', String(length));
      path.setAttribute('stroke-dashoffset', String(length));
    }
    if (reduced.matches) return;
    for (const packet of packets) {
      const dot = document.createElementNS(SVG, 'circle');
      dot.setAttribute('r', '2.5');
      dot.style.offsetPath = `path('${packet.d}')`;
      dot.style.animation = `kro-packet ${packet.duration.toFixed(2)}s linear ${packet.delay.toFixed(2)}s infinite`;
      svg!.append(dot);
    }
  }

  let frame = 0;
  let settle = 0;
  function remeasure(afterOpening = false) {
    cancelAnimationFrame(frame);
    frame = requestAnimationFrame(measure);
    if (afterOpening) {
      clearTimeout(settle);
      settle = window.setTimeout(measure, SETTLED_MS);
    }
  }

  // --- opening, search --------------------------------------------------------------------------

  for (const rack of racks) {
    rack.head.addEventListener('click', () => {
      if (chosen.has(rack.id)) chosen.delete(rack.id);
      else chosen.add(rack.id);
      apply();
      remeasure(true);
    });
  }

  query?.addEventListener('input', () => {
    const text = query.value.trim().toLowerCase();
    searching = text !== '';
    let found = false;
    for (const rack of racks) {
      rack.hits = 0;
      for (const skill of rack.skills) {
        const hit = searching && (skill.textContent ?? '').toLowerCase().includes(text);
        skill.classList.toggle('is-hit', hit);
        skill.classList.toggle('is-miss', searching && !hit);
        if (hit) rack.hits += 1;
      }
      if (rack.hits > 0) found = true;
    }
    if (empty) empty.hidden = !searching || found;
    apply();
    remeasure(true);
  });

  apply();
  remeasure();
  new ResizeObserver(() => remeasure()).observe(topology);
  vertical.addEventListener('change', () => remeasure(true));
  reduced.addEventListener('change', () => remeasure());
  void document.fonts?.ready.then(() => remeasure());
}
