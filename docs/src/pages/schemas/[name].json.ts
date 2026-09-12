import type { APIRoute, GetStaticPaths } from 'astro';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

const names = [
  'runtime-config.schema',
  'runtime-http.schema',
  'runtime-ssh.schema',
  'runtime-tcp.schema',
  'runtime-telnet.schema',
  'runtime-mcp.schema',
];

export const getStaticPaths: GetStaticPaths = () => names.map((name) => ({ params: { name } }));

export const GET: APIRoute = async ({ params }) => {
  if (!params.name || !names.includes(params.name)) return new Response('Not found', { status: 404 });
  const file = fileURLToPath(new URL(`../../../../specs/${params.name}.json`, import.meta.url));
  const body = await readFile(file, 'utf8');
  return new Response(body, { headers: { 'Content-Type': 'application/schema+json; charset=utf-8' } });
};
