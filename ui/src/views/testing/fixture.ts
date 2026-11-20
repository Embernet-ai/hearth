import fs from 'node:fs';
import { page } from './draw';

/** The page data the server sends for a fixture (internal/web/fixtures_test.go), on top of a signed-in person. */
export function fixture(name: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  const raw = fs.readFileSync(new URL(`../fixtures/${name}.json`, import.meta.url), 'utf8');
  return page({ ...JSON.parse(raw), ...extra });
}
