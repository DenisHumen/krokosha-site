// The text side of /map: the table of a route and the card of a network. Built with DOM calls and
// textContent only — names come from the registries of the internet, and nobody vouches for them.

import type { Site } from '../../lib/data.ts';
import type { NetworkJSON, RouteJSON } from './api.ts';
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

  target.append(head, wrap, notes);
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
