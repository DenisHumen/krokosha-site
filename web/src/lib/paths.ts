// Where the data lives. `web/` is one zone of the repository; content/ and mock/ are its siblings.

import { existsSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';

/** Repository root: the closest parent directory that contains content/site.yaml. */
export function findRepoRoot(from: string = process.cwd()): string {
  const override = process.env.KROKOSHA_ROOT;
  if (override) return resolve(override);
  let dir = resolve(from);
  for (;;) {
    if (existsSync(join(dir, 'content', 'site.yaml'))) return dir;
    const parent = dirname(dir);
    if (parent === dir) {
      throw new Error(`content/site.yaml not found in "${from}" or any parent directory`);
    }
    dir = parent;
  }
}

export function repoPaths(root: string = findRepoRoot()) {
  return {
    root,
    siteYaml: join(root, 'content', 'site.yaml'),
    skillsYaml: join(root, 'content', 'skills.yaml'),
    projectsYaml: join(root, 'content', 'projects.yaml'),
    githubJson: join(root, 'content', 'generated', 'github.json'),
    mockDir: join(root, 'mock'),
  };
}
