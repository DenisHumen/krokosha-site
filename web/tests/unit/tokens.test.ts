// design/tokens.css carries the light theme twice: for the toggle ([data-theme='light']) and for the
// system's choice (prefers-color-scheme). The two must not drift apart.

import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { repoPaths } from '../../src/lib/paths.ts';

const css = readFileSync(join(repoPaths().root, 'design', 'tokens.css'), 'utf8');

function declarations(block: RegExp): string[] {
  return (block.exec(css)?.[1] ?? '')
    .split(';')
    .map((line) => line.trim())
    .filter(Boolean)
    .sort();
}

describe('design/tokens.css', () => {
  it('uses the same light values for the toggle and for the system theme', () => {
    const toggled = declarations(/:root\[data-theme='light'\]\s*\{([^}]*)\}/);
    const system = declarations(/:root:not\(\[data-theme='dark'\]\)\s*\{([^}]*)\}/);
    expect(toggled.length).toBeGreaterThan(10);
    expect(system).toEqual(toggled);
  });
});
