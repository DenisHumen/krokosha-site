// Long texts live in content/pages/<name>.<lang>.md so that Denis can edit them without touching code.

import { defineCollection } from 'astro:content';
import { glob } from 'astro/loaders';
import { z } from 'astro/zod';

const pages = defineCollection({
  loader: glob({
    base: '../content/pages',
    pattern: '*.{en,uk,ru}.md',
    // `privacy.uk.md` → `privacy.uk` (the default would slugify the dot away)
    generateId: ({ entry }) => entry.replace(/\.md$/, ''),
  }),
  schema: z.object({
    title: z.string().min(1),
    /** Publication date; `TODO` until the text is final. YAML turns a bare date into a Date object. */
    updated: z.union([z.date(), z.string()]),
    /** A `draft` page is built but kept out of search results. */
    status: z.enum(['draft', 'published']),
  }),
});

export const collections = { pages };
