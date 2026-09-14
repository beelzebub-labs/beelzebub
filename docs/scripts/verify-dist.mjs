import { readdir, readFile } from 'node:fs/promises';
import { join, relative, resolve } from 'node:path';

const root = resolve(new URL('../dist', import.meta.url).pathname);
const siteOrigin = new URL(process.env.SITE_URL || 'https://docs.beelzebub.ai').origin;
const failures = [];

async function walk(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const file = join(dir, entry.name);
    if (entry.isDirectory()) files.push(...await walk(file));
    else files.push(file);
  }
  return files;
}

function candidates(href, currentFile) {
  const currentDir = relative(root, currentFile).replace(/index\.html$/, '');
  const resolved = new URL(href, `https://docs.invalid/${currentDir}`).pathname;
  if (!resolved || href.startsWith('http') || href.startsWith('mailto:') || href.startsWith('javascript:')) return [];
  const clean = resolved.replace(/^\//, '');
  return [
    join(root, clean),
    join(root, clean, 'index.html'),
    join(root, `${clean}.html`),
  ];
}

for (const file of await walk(root)) {
  if (!file.endsWith('.html')) continue;
  const html = await readFile(file, 'utf8');
  const head = html.match(/<head>([\s\S]*?)<\/head>/)?.[1] ?? '';
  const body = html.match(/<body[^>]*>([\s\S]*?)<\/body>/)?.[1] ?? '';
  if (!/<title>[^<]+<\/title>/.test(head)) failures.push(`${relative(root, file)} -> missing title in head`);
  if (!/<meta name="description" content="[^"]+"/.test(head)) failures.push(`${relative(root, file)} -> missing description in head`);
  if (!head.includes(`<link rel="canonical" href="${siteOrigin}/`)) failures.push(`${relative(root, file)} -> missing canonical URL for ${siteOrigin}`);
  if (/<title>|<meta (?:name="description"|property="og:)/.test(body)) failures.push(`${relative(root, file)} -> document metadata rendered in body`);
  for (const href of html.matchAll(/(?:href|src)="([^"]+)"/g)) {
    const options = candidates(href[1], file);
    if (options.length && !(await Promise.all(options.map(async (candidate) => {
      try { await readFile(candidate); return true; } catch { return false; }
    }))).some(Boolean)) {
      failures.push(`${relative(root, file)} -> ${href[1]}`);
    }
  }
}

const firstService = await readFile(join(root, 'getting-started', 'first-service', 'index.html'), 'utf8');
if (!firstService.includes('https://github.com/beelzebub-labs/beelzebub/blob/main/docs/content/docs/getting-started/first-service.mdx')) {
  failures.push('first service page -> invalid or missing Edit on GitHub link');
}
if (!firstService.includes('href="/examples/http-lab.yaml"')) failures.push('first service page -> missing maintained example download');

const architecture = await readFile(join(root, 'concepts', 'architecture', 'index.html'), 'utf8');
if (!architecture.includes('class="runtime-architecture not-prose"')) {
  failures.push('runtime architecture page -> missing static architecture diagram');
}
if (architecture.includes('Rendering diagram') || architecture.includes('data-mermaid-chart')) {
  failures.push('runtime architecture page -> contains a client-rendered diagram placeholder');
}

const blueprint = await readFile(join(root, 'plugins', 'blueprint', 'index.html'), 'utf8');
if (!blueprint.includes('href="https://github.com/beelzebub-labs/beelzebub-plugin-blueprint"')) {
  failures.push('plugin blueprint page -> missing maintained repository link');
}
if (!blueprint.includes('id="beyond-beelzebub-runtime"')) {
  failures.push('plugin blueprint page -> missing Beyond Beelzebub Runtime section');
}

for (const route of [
  'index.html',
  '404.html',
  'api/search',
  'llms.txt',
  'llms-full.txt',
  'favicon.svg',
  'favicon.ico',
  'favicon-96x96.png',
  'apple-touch-icon.png',
  'logo.png',
  'site.webmanifest',
  'web-app-manifest-192x192.png',
  'web-app-manifest-512x512.png',
  'schemas/runtime-http.schema.json',
  'examples/http-lab.yaml',
]) {
  try { await readFile(join(root, route)); } catch { failures.push(`missing generated route: ${route}`); }
}

if (failures.length) {
  console.error(failures.map((failure) => `✗ ${failure}`).join('\n'));
  process.exit(1);
}
console.log('✓ generated pages, internal links, assets, search, schemas, examples, and machine-readable routes verified');
