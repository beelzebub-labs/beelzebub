import type { APIRoute } from 'astro';
import { getCollection } from 'astro:content';

export const GET: APIRoute = async () => {
  const pages = (await getCollection('docs')).sort((a, b) => a.id.localeCompare(b.id));
  const pageUrl = (id: string) => id === 'index' ? '/' : `/${id.replace(/\.(md|mdx)$/, '')}`;
  const body = [
    '# Beelzebub Documentation',
    '',
    '> Open-source deception runtime for SSH, HTTP, TCP, TELNET, and MCP.',
    '',
    ...pages.map((page) => `- [${page.data.title}](${pageUrl(page.id)}): ${page.data.description}`),
    '',
  ].join('\n');
  return new Response(body, { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
};
