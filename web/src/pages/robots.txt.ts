// robots.txt. The admin path is deliberately not mentioned here (brief B6): it is protected
// by authentication and `noindex`, listing it would only advertise it.

import type { APIRoute } from 'astro';
import { absoluteUrl } from '../lib/seo.ts';

export const GET: APIRoute = ({ site }) => {
  if (!site) throw new Error('astro.config: "site" is required for robots.txt');
  const body = [
    'User-agent: *',
    'Allow: /',
    'Disallow: /api/',
    '',
    `Sitemap: ${absoluteUrl(site, '/sitemap.xml')}`,
    '',
  ];
  return new Response(body.join('\n'), {
    headers: { 'Content-Type': 'text/plain; charset=utf-8' },
  });
};
