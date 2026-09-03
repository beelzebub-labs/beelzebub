import type { APIRoute, GetStaticPaths } from 'astro';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

const names = ['http-lab', 'ssh-llm', 'mcp-tripwire', 'tcp-redis', 'core-observability'];

export const getStaticPaths: GetStaticPaths = () => names.map((name) => ({ params: { name } }));

export const GET: APIRoute = async ({ params }) => {
  if (!params.name || !names.includes(params.name)) return new Response('Not found', { status: 404 });
  const file = fileURLToPath(new URL(`../../../examples/${params.name}.yaml`, import.meta.url));
  const body = await readFile(file, 'utf8');
  return new Response(body, { headers: { 'Content-Type': 'text/yaml; charset=utf-8' } });
};
