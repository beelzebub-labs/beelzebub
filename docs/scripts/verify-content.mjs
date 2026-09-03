import { cp, mkdir, mkdtemp, readdir, readFile, rm } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';

const docsRoot = resolve(new URL('..', import.meta.url).pathname);
const repoRoot = resolve(docsRoot, '..');
const contentRoot = join(docsRoot, 'content', 'docs');
const required = [
  'index.mdx',
  'getting-started/quick-start.mdx',
  'reference/configuration.mdx',
  'protocols/mcp.mdx',
  'plugins/authoring.mdx',
  'plugins/blueprint.mdx',
  'operations/production-safety.mdx',
];

const failures = [];
for (const file of required) {
  try { await readFile(join(contentRoot, file)); } catch { failures.push(`missing required page: ${file}`); }
}

async function walk(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) files.push(...await walk(path));
    else if (/\.(md|mdx)$/.test(entry.name)) files.push(path);
  }
  return files;
}

for (const file of await walk(contentRoot)) {
  const text = await readFile(file, 'utf8');
  const match = text.match(/^title:\s*(.+)$/m);
  const title = match?.[1].trim() ?? '';
  if (!match || !/^[A-Z0-9]/.test(title) || title.split(/\s+/).some((word) => /^[A-Za-z]/.test(word) && word[0] !== word[0].toUpperCase())) {
    failures.push(`title must use Title Case: ${file}`);
  }
  if (!/^description:\s*\S/m.test(text)) failures.push(`missing description: ${file}`);
}

const blueprint = await readFile(join(contentRoot, 'plugins', 'blueprint.mdx'), 'utf8');
if (!blueprint.includes('https://github.com/beelzebub-labs/beelzebub-plugin-blueprint')) {
  failures.push('plugin blueprint page must link to the maintained blueprint repository');
}

for (const file of [
  'index.mdx',
  'getting-started/installation.mdx',
  'protocols/index.mdx',
  'plugins/authoring.mdx',
  'operations/integrations.mdx',
]) {
  const text = await readFile(join(contentRoot, file), 'utf8');
  if (!text.includes('## Beyond Beelzebub Runtime\n\n<BeyondRuntime />')) {
    failures.push(`missing Beyond Beelzebub Runtime section: ${file}`);
  }
}

const exampleDir = join(docsRoot, 'examples');
const serviceExamples = ['http-lab.yaml', 'ssh-llm.yaml', 'mcp-tripwire.yaml', 'tcp-redis.yaml'];
const tempDir = await mkdtemp(join('/tmp', 'beelzebub-docs-'));
try {
  const servicesDir = join(tempDir, 'services');
  await mkdir(servicesDir);
  for (const name of serviceExamples) await cp(join(exampleDir, name), join(servicesDir, name));
  const goEnv = {
    ...process.env,
    GOCACHE: join('/tmp', 'beelzebub-docs-go-cache'),
    GOMODCACHE: join('/tmp', 'beelzebub-go-mod'),
  };
  const run = (command, args) => spawnSync(command, args, {
    cwd: repoRoot,
    env: goEnv,
    encoding: 'utf8',
    stdio: 'pipe',
  });

  const schemaResult = run('go', ['run', './cmd/validate-specs', '-configs', servicesDir]);
  if (schemaResult.status !== 0) failures.push(`documentation examples failed schema validation:\n${schemaResult.stdout}${schemaResult.stderr}`);

  const binary = join(tempDir, 'beelzebub');
  const buildResult = run('go', ['build', '-o', binary, '.']);
  if (buildResult.status !== 0) {
    failures.push(`could not build the CLI for documentation verification:\n${buildResult.stdout}${buildResult.stderr}`);
  } else {
    const validationResult = run(binary, [
      'validate',
      '--conf-core', join(exampleDir, 'core-observability.yaml'),
      '--conf-services', servicesDir,
    ]);
    if (validationResult.status !== 0) failures.push(`documentation examples failed full validation:\n${validationResult.stdout}${validationResult.stderr}`);

    const helpChecks = [
      { args: ['--help'], expected: ['--conf-core', '--conf-services', '--log-level', 'plugin', 'run', 'validate', 'version'] },
      { args: ['run', '--help'], expected: ['--mem-limit-mib'] },
      { args: ['plugin', 'install', '--help'], expected: ['--ref', '--token', '--force', '--no-build'] },
      { args: ['plugin', 'update', '--help'], expected: ['--token'] },
    ];
    for (const check of helpChecks) {
      const result = run(binary, check.args);
      const output = `${result.stdout}${result.stderr}`;
      for (const expected of check.expected) {
        if (result.status !== 0 || !output.includes(expected)) failures.push(`CLI help ${check.args.join(' ')} is missing ${expected}`);
      }
    }
  }
} finally {
  await rm(tempDir, { recursive: true, force: true });
}

if (failures.length) {
  console.error(failures.map((failure) => `✗ ${failure}`).join('\n'));
  process.exit(1);
}
console.log(`✓ verified required pages, service and core examples, CLI reference, and Title Case metadata`);
