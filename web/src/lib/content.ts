// Reads and validates the data the site is built from:
//   content/*.yaml                  — edited by Denis
//   content/generated/github.json   — written by the GitHub sync every 6 hours
// Without a sync result (CI, a fresh clone) the GitHub snapshot from mock/ is used.

import { existsSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { parse as parseYaml } from 'yaml';
import { z } from 'zod';
import { findIncompleteTranslations } from '../i18n/localize.ts';
import { DEFAULT_LOCALE } from '../i18n/locales.ts';
import { repoPaths } from './paths.ts';
import {
  githubDataSchema,
  projectsConfigSchema,
  siteSchema,
  skillsSchema,
  type GithubData,
  type ProjectsConfig,
  type SiteContent,
  type SkillsContent,
} from './schema.ts';

export interface Content {
  site: SiteContent;
  skills: SkillsContent;
  projectsConfig: ProjectsConfig;
  github: GithubData;
  /** `generated` — real sync result; `snapshot` — fallback from mock/. */
  githubSource: 'generated' | 'snapshot';
}

function validate<T extends z.ZodType>(schema: T, data: unknown, file: string): z.infer<T> {
  const incomplete = findIncompleteTranslations(data);
  if (incomplete.length > 0) {
    throw new Error(
      `${file}: every translated text needs all of en, uk, ru. Incomplete:\n  ${incomplete.join('\n  ')}`,
    );
  }
  const result = schema.safeParse(data);
  if (!result.success) {
    throw new Error(`${file} is invalid:\n${z.prettifyError(result.error)}`);
  }
  return result.data;
}

function readYaml<T extends z.ZodType>(schema: T, path: string, root: string): z.infer<T> {
  const file = relative(root, path).replaceAll('\\', '/');
  return validate(schema, parseYaml(readFileSync(path, 'utf8')), file);
}

function readJson<T extends z.ZodType>(schema: T, path: string, root: string): z.infer<T> {
  const file = relative(root, path).replaceAll('\\', '/');
  return validate(schema, JSON.parse(readFileSync(path, 'utf8')), file);
}

export interface LoadOptions {
  root?: string;
  /** `snapshot` ignores the sync result: used to regenerate mock/ deterministically. */
  github?: 'auto' | 'snapshot';
}

export function loadContent({ root, github = 'auto' }: LoadOptions = {}): Content {
  const paths = repoPaths(root);
  const hasSync = github === 'auto' && existsSync(paths.githubJson);
  const githubPath = hasSync
    ? paths.githubJson
    : join(paths.mockDir, DEFAULT_LOCALE, 'projects.json');
  return {
    site: readYaml(siteSchema, paths.siteYaml, paths.root),
    skills: readYaml(skillsSchema, paths.skillsYaml, paths.root),
    projectsConfig: readYaml(projectsConfigSchema, paths.projectsYaml, paths.root),
    github: readJson(githubDataSchema, githubPath, paths.root),
    githubSource: hasSync ? 'generated' : 'snapshot',
  };
}

let cached: Content | undefined;

/** Same as `loadContent()`, read once per build. */
export function getContent(): Content {
  if (!cached) {
    cached = loadContent();
    if (cached.githubSource === 'snapshot') {
      console.warn(
        '[content] content/generated/github.json not found — using the GitHub snapshot from mock/.',
      );
    }
  }
  return cached;
}
