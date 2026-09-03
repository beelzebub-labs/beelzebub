import type { APIRoute } from 'astro';
import { getCollection } from 'astro:content';

export async function getStaticPaths() {
  return (await getCollection('docs')).map((page) => ({
    params: { slug: page.id.replace(/\.(md|mdx)$/, '') },
    props: { body: page.body },
  }));
}

export const GET: APIRoute = ({ props }) => new Response(props.body, {
  headers: { 'Content-Type': 'text/markdown; charset=utf-8' },
});
