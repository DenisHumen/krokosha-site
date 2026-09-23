// The text side of /map: the table of a route and the card of a network. Built with DOM calls and
// textContent only — names come from the registries of the internet, and nobody vouches for them.

import type { Site } from '../../lib/data.ts';
import type { NetworkJSON, RouteJSON, TraceJSON } from './api.ts';
import { ms, ports } from './api.ts';

export type MapTexts = Site['map'];

function element<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className?: string,
  text?: string,
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

/** The form of a word for a number: forms are [one, other] in English, [one, few, many] in Slavic. */
export function plural(forms: readonly string[], count: number, locale: string): string {
  const rule = new Intl.PluralRules(locale).select(count);
  if (forms.length === 2) return (rule === 'one' ? forms[0] : forms[1]) ?? '';
  return (rule === 'one' ? forms[0] : rule === 'few' ? forms[1] : forms[2]) ?? '';
}

/** «{ip} is anycast…» with the placeholder filled. */
function fill(template: string, values: Record<string, string>): string {
  return template.replace(/\{(\w+)\}/g, (whole, key: string) => values[key] ?? whole);
}

/** The table of a route: one row per network, where it hands the traffic on and how long it took. */
export function renderRoute(
  target: HTMLElement,
  route: RouteJSON,
  texts: MapTexts,
  locale: string,
): void {
  const { panel } = texts;
  target.replaceChildren();

  const head = element('header', 'route-head');
  head.append(
    showSwitch('model', texts, true),
    element('p', 'eyebrow', route.kind === 'observed' ? panel.observed : panel.estimated),
    (() => {
      const summary = element('p', 'route-summary');
      summary.append(
        element('b', 'mono', `≈ ${ms(route.rtt)} ${panel.ms}`),
        document.createTextNode(
          ` ${panel.rtt} · ${route.hops.length} ${plural(panel.networks, route.hops.length, locale)}`,
        ),
      );
      return summary;
    })(),
    element('p', 'route-ends mono', `${route.from.ip} → ${route.to.ip}`),
  );

  const table = element('table', 'route-table');
  const caption = element('caption', 'visually-hidden', `${route.from.ip} → ${route.to.ip}`);
  const thead = element('thead');
  const headRow = element('tr');
  const { columns } = panel;
  for (const title of [
    columns.hop,
    columns.network,
    columns.relation,
    columns.meeting,
    columns.ports,
    columns.delay,
    columns.loss,
  ]) {
    const cell = element('th', undefined, title);
    cell.scope = 'col';
    headRow.append(cell);
  }
  thead.append(headRow);

  const body = element('tbody');
  route.hops.forEach((hop, i) => {
    const row = element('tr');
    row.dataset['hop'] = String(i);

    const number = element('td', 'mono hop-number', String(i + 1).padStart(2, '0'));

    const network = element('td', 'hop-network');
    network.append(element('span', 'mono hop-asn', `AS${hop.asn}`));
    if (hop.name) network.append(element('span', 'hop-name', hop.name));
    if (hop.country) network.append(element('span', 'mono muted hop-country', hop.country));

    const relation = element(
      'td',
      'hop-relation',
      i === 0 ? panel.start : panel.rel[hop.rel ?? 'unknown'],
    );

    const meeting = element('td', 'hop-meeting');
    const where = hop.meet;
    if (where) {
      const place = [where.name, [where.city, where.country].filter(Boolean).join(', ')]
        .filter(Boolean)
        .join(' · ');
      meeting.append(element('span', 'meet-kind mono', panel.meet[where.kind]));
      if (place) meeting.append(element('span', 'meet-place', place));
    } else {
      meeting.append(element('span', 'muted', '—'));
    }

    const speeds = where
      ? [ports(where.speeds[0], where.ports[0]), ports(where.speeds[1], where.ports[1])]
      : ['', ''];
    const port = element(
      'td',
      'mono hop-ports',
      speeds[0] || speeds[1] ? `${speeds[0] || '?'} ↔ ${speeds[1] || '?'}` : '—',
    );

    const delay = element(
      'td',
      'mono hop-delay',
      `≈ ${ms(where ? where.rtt : route.rtt)} ${panel.ms}`,
    );
    const loss = element('td', 'muted hop-loss', panel.not_measured);

    row.append(number, network, relation, meeting, port, delay, loss);
    body.append(row);
  });
  table.append(caption, thead, body);
  // On a phone the table scrolls sideways: the keyboard must be able to scroll it too.
  const wrap = element('div', 'route-table-wrap');
  wrap.tabIndex = 0;
  wrap.setAttribute('role', 'region');
  wrap.setAttribute('aria-label', `${route.from.ip} → ${route.to.ip}`);
  wrap.append(table);

  const notes = element('div', 'route-notes');
  if (route.to.anycast) {
    notes.append(element('p', 'route-note', fill(panel.anycast, { ip: route.to.ip })));
  }
  notes.append(element('p', 'route-note muted', panel.note));

  // The way measured from the site's server: asked for on request, it takes a few seconds.
  const measure = element('button', 'pill route-measure', texts.trace.measure);
  measure.type = 'button';
  measure.dataset['routeTrace'] = route.to.ip;
  const measured = element('div', 'route-part');
  measured.dataset['traceResult'] = '';
  measured.hidden = true;

  const model = element('div', 'route-part');
  model.append(head, wrap, notes, measure);
  target.append(model, measured);
}

/** The switch that puts one of the two ways on the map; hidden while there is only one. */
function showSwitch(part: 'model' | 'trace', texts: MapTexts, pressed: boolean): HTMLButtonElement {
  const button = element(
    'button',
    'route-show mono',
    pressed ? texts.trace.shown : texts.trace.show,
  );
  button.type = 'button';
  button.dataset['show'] = part;
  button.setAttribute('aria-pressed', String(pressed));
  button.hidden = part === 'model';
  return button;
}

/** Marks which way is on the map. */
export function markShown(target: HTMLElement, part: 'model' | 'trace', texts: MapTexts): void {
  for (const button of target.querySelectorAll<HTMLButtonElement>('[data-show]')) {
    const pressed = button.dataset['show'] === part;
    button.setAttribute('aria-pressed', String(pressed));
    button.textContent = pressed ? texts.trace.shown : texts.trace.show;
    button.hidden = false;
  }
}

function median(values: number[]): number | null {
  if (values.length === 0) return null;
  const sorted = [...values].sort((a, b) => a - b);
  const middle = Math.floor(sorted.length / 2);
  return sorted.length % 2 ? sorted[middle]! : (sorted[middle - 1]! + sorted[middle]!) / 2;
}

/** The way measured from the site's server: one row per TTL, who answered and how fast. */
export function renderTrace(
  target: HTMLElement,
  trace: TraceJSON,
  texts: MapTexts,
  locale: string,
): void {
  const t = texts.trace;
  const { panel } = texts;
  target.replaceChildren();

  const last = trace.hops.at(-1);
  const toAddress = trace.reached && last ? median(last.rtts) : null;
  const handshake = trace.connect ? median(trace.connect.rtts) : null;
  const time = toAddress ?? handshake;

  const head = element('header', 'route-head');
  const summary = element('p', 'route-summary');
  if (time !== null)
    summary.append(
      element('b', 'mono', `${ms(time)} ${panel.ms}`),
      document.createTextNode(` ${panel.rtt} · `),
    );
  summary.append(
    document.createTextNode(`${trace.hops.length} ${plural(t.hops, trace.hops.length, locale)}`),
  );
  const from = [trace.from.ip, trace.from.asn ? `AS${trace.from.asn}` : '', trace.from.name]
    .filter(Boolean)
    .join(' ');
  head.append(
    showSwitch('trace', texts, false),
    element('p', 'eyebrow', t.heading),
    summary,
    element('p', 'route-ends mono', `${from} → ${trace.to.ip}`),
  );

  const table = element('table', 'route-table');
  table.append(element('caption', 'visually-hidden', `${t.heading}: ${from} → ${trace.to.ip}`));
  const thead = element('thead');
  const headRow = element('tr');
  for (const title of [
    t.columns.hop,
    t.columns.address,
    t.columns.network,
    t.columns.answered,
    t.columns.time,
  ]) {
    const cell = element('th', undefined, title);
    cell.scope = 'col';
    headRow.append(cell);
  }
  thead.append(headRow);

  const body = element('tbody');
  for (const hop of trace.hops) {
    const row = element('tr');
    row.dataset['ttl'] = String(hop.ttl);

    const address = element('td', 'hop-network');
    if (hop.ip) {
      address.append(element('span', 'mono', hop.ip));
      if (hop.host) address.append(element('span', 'mono muted hop-host', hop.host));
      if (hop.others?.length)
        address.append(element('span', 'mono muted hop-host', `+ ${hop.others.join(', ')}`));
    } else {
      address.append(element('span', 'muted', `* ${t.silent}`));
    }

    const network = element('td', 'hop-network');
    if (hop.asn) {
      network.append(element('span', 'mono hop-asn', `AS${hop.asn}`));
      if (hop.name) network.append(element('span', 'hop-name', hop.name));
    }
    if (hop.ix) network.append(element('span', 'meet-kind mono', `${t.exchange} · ${hop.ix}`));
    if (!hop.asn && !hop.ix) network.append(element('span', 'muted', '—'));

    const answered = element('td', 'hop-answered mono', `${hop.rtts.length}/${hop.sent}`);
    if (hop.reached) answered.append(element('span', 'muted hop-host', t.arrived));
    if (hop.refused) answered.append(element('span', 'muted hop-host', t.refused));

    const middle = median(hop.rtts);
    const delay = element(
      'td',
      'mono hop-delay',
      middle === null ? '—' : `${ms(middle)} ${panel.ms}`,
    );
    if (hop.rtts.length > 1)
      delay.title = hop.rtts.map((value) => `${ms(value)} ${panel.ms}`).join(' · ');

    row.append(
      element('td', 'mono hop-number', String(hop.ttl).padStart(2, '0')),
      address,
      network,
      answered,
      delay,
    );
    body.append(row);
  }
  table.append(thead, body);
  const wrap = element('div', 'route-table-wrap');
  wrap.tabIndex = 0;
  wrap.setAttribute('role', 'region');
  wrap.setAttribute('aria-label', `${t.heading}: ${trace.to.ip}`);
  wrap.append(table);

  const notes = element('div', 'route-notes');
  if (trace.connect && handshake !== null) {
    notes.append(
      element(
        'p',
        'route-note mono',
        fill(t.tcp, {
          port: String(trace.connect.port),
          answered: String(trace.connect.rtts.length),
          sent: String(trace.connect.sent),
          ms: ms(handshake),
        }),
      ),
    );
  } else {
    notes.append(element('p', 'route-note', t.no_tcp));
  }
  if (!trace.reached) notes.append(element('p', 'route-note', t.not_reached));
  notes.append(element('p', 'route-note muted', t.note));

  target.append(head, wrap, notes);
  target.hidden = false;
}

/** The card of one network: its size, its neighbours, its ports at exchange points. */
export function renderNetwork(
  target: HTMLElement,
  network: NetworkJSON,
  total: number,
  texts: MapTexts,
  locale: string,
): void {
  const t = texts.network;
  const number = new Intl.NumberFormat(locale);
  target.replaceChildren();

  const head = element('header', 'network-head');
  const title = element('h2', 'network-title');
  title.append(element('span', 'mono', `AS${network.asn}`));
  if (network.name) title.append(document.createTextNode(` ${network.name}`));
  head.append(
    element('p', 'eyebrow', [t.heading, network.country, network.type].filter(Boolean).join(' · ')),
    title,
  );

  const facts = element('dl', 'network-facts');
  const fact = (label: string, value: string) => {
    const box = element('div');
    box.append(element('dt', 'muted', label), element('dd', 'mono', value));
    facts.append(box);
  };
  fact(t.cone, number.format(network.cone));
  fact(t.rank, `${number.format(network.rank)} / ${number.format(total)}`);
  fact(t.customers, number.format(network.customers));
  fact(t.providers, number.format(network.providers));
  fact(t.peers, number.format(network.peers));

  const lists = element('div', 'network-lists');
  const list = (label: string, items: { asn: number; name: string }[]) => {
    if (items.length === 0) return;
    const box = element('div', 'network-list');
    box.append(element('h3', 'eyebrow', label));
    const ul = element('ul');
    for (const item of items) {
      const li = element('li');
      li.append(element('span', 'mono', `AS${item.asn}`), document.createTextNode(` ${item.name}`));
      ul.append(li);
    }
    box.append(ul);
    lists.append(box);
  };
  list(t.providers, network.neighbours.providers.slice(0, 8));
  list(t.peers, network.neighbours.peers.slice(0, 8));
  list(t.customers, network.neighbours.customers.slice(0, 8));
  if (network.exchanges.length > 0) {
    const box = element('div', 'network-list');
    box.append(element('h3', 'eyebrow', t.exchanges));
    const ul = element('ul');
    for (const port of network.exchanges.slice(0, 10)) {
      const li = element('li');
      li.append(
        element('span', 'mono', ports(port.speed, port.ports)),
        document.createTextNode(` ${port.name}`),
        element('span', 'muted', ` · ${port.city}`),
      );
      ul.append(li);
    }
    box.append(ul);
    lists.append(box);
  }

  target.append(head, facts, lists);
}
