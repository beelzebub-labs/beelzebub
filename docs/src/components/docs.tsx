import { DocsLayout } from 'fumadocs-ui/layouts/docs';
import { DocsPage, type DocsPageProps } from 'fumadocs-ui/page';
import type { Root } from 'fumadocs-core/page-tree';
import type { ReactNode } from 'react';
import { navigate } from 'astro:transitions/client';
import { RootProvider } from 'fumadocs-ui/provider/astro';
import type { AstroProviderProps } from 'fumadocs-core/framework/astro';
import SearchDialog from './search';

export function Docs({
  tree,
  children,
  pathname,
  params,
  page,
}: {
  tree: Root;
  children: ReactNode;
  pathname: string;
  params: AstroProviderProps['params'];
  page?: DocsPageProps;
}) {
  return (
    <RootProvider pathname={pathname} params={params} navigate={navigate} search={{ SearchDialog }}>
      <DocsLayout
        tree={tree}
        nav={{
          title: (
            <span className="flex items-center gap-2.5">
              <img
                src="/logo.png"
                width={28}
                height={28}
                alt=""
                aria-hidden="true"
                className="beelzebub-brand-mark size-7 shrink-0 object-contain"
              />
              <span>Beelzebub</span>
            </span>
          ),
        }}
        githubUrl="https://github.com/beelzebub-labs/beelzebub"
      >
        <DocsPage {...page}>{children}</DocsPage>
      </DocsLayout>
    </RootProvider>
  );
}
