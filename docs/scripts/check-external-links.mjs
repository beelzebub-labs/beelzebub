import { readdir, readFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';

const root = resolve(new URL('../dist', import.meta.url).pathname);
const siteOrigin = new URL(process.env.SITE_URL || 'https://docs.beelzebub.ai').origin;
const urls = new Set();

async function walk(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  return (await Promise.all(entries.map((entry) => {
    const path = join(dir, entry.name);
    return entry.isDirectory() ? walk(path) : path;
  }))).flat();
}

for (const file of await walk(root)) {
  if (!file.endsWith('.html')) continue;
  const html = await readFile(file, 'utf8');
  for (const match of html.matchAll(/href="(https?:\/\/[^"#]+)(?:#[^"]*)?"/g)) {
    const url = match[1].replaceAll('&amp;', '&');
    if (new URL(url).origin !== siteOrigin) urls.add(url);
  }
}

const failures = [];
async function check(url) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 15_000);
  try {
    let response = await fetch(url, {
      method: 'HEAD',
      redirect: 'follow',
      signal: controller.signal,
      headers: { 'user-agent': 'beelzebub-docs-link-checker/1.0' },
    });
    if (response.status === 405 || response.status === 501) {
      response = await fetch(url, {
        method: 'GET',
        redirect: 'follow',
        signal: controller.signal,
        headers: { 'user-agent': 'beelzebub-docs-link-checker/1.0' },
      });
    }
    if (!response.ok) failures.push(`${response.status} ${url}`);
  } catch (error) {
    failures.push(`${error instanceof Error ? error.message : String(error)} ${url}`);
  } finally {
    clearTimeout(timeout);
  }
}

const pending = [...urls].sort();
for (let i = 0; i < pending.length; i += 6) await Promise.all(pending.slice(i, i + 6).map(check));

if (failures.length) {
  console.error(failures.map((failure) => `✗ ${failure}`).join('\n'));
  process.exit(1);
}
console.log(`✓ verified ${urls.size} external links`);
