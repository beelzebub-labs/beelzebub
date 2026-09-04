import type { APIRoute } from 'astro';
import { getCollection } from 'astro:content';

export const GET: APIRoute = async () => {
  const pages = (await getCollection('docs')).sort((a, b) => a.id.localeCompare(b.id));
  const body = pages.map((page) => `# ${page.data.title}\n\n${page.data.description}\n\n${page.body}`).join('\n\n---\n\n');
  return new Response(body, { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
};
