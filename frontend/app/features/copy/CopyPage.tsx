import { BackLink } from "../../components/ui/BackLink";
import type { ReactNode } from "react";
import { Link } from "react-router";
import type { CopyDocument, RenderedCopy } from "../../lib/markdown";
import { ArticleLayout, RailSection } from "../../components/ui/Page";
import { PhoneBar } from "../../components/shell/PhoneBar";
import { IconChevronLeft } from "../../components/icons";

/**
 * Legal and policy pages, read like articles: the document on the page in one column, its facts in the
 * left rail and its outline in the right (phones get the facts above the text and no outline).
 */
export function CopyPage({
  doc,
  rendered,
  eyebrow,
  footer,
  aside,
}: {
  doc: CopyDocument;
  rendered: RenderedCopy;
  eyebrow?: ReactNode;
  footer?: ReactNode;
  aside?: ReactNode;
}) {
  const facts = (["版本", "生效日期", "运营主体", "联系方式", "备案号"] as const).filter((k) => {
    return Boolean(doc.meta[k]);
  });

  const info = facts.length > 0 && (
    <RailSection title="文档信息">
      <dl className="space-y-2 text-[12.5px]">
        {facts.map((k) => (
          <div key={k}>
            <dt className="text-ink-4">{k}</dt>
            <dd className="mt-0.5 text-ink-2">{doc.meta[k]}</dd>
          </div>
        ))}
      </dl>
    </RailSection>
  );

  const outline = rendered.outline.length > 2 && (
    <RailSection title="目录">
      <nav aria-label="目录">
        <ol className="-ml-px space-y-0.5 border-l border-line">
          {rendered.outline.map((o) => (
            <li key={o.id}>
              <a
                href={`#${o.id}`}
                className="-ml-px block border-l border-transparent py-1 pl-3 text-[12.5px] leading-snug text-ink-3 transition-colors hover:border-accent hover:text-ink"
              >
                {o.text}
              </a>
            </li>
          ))}
        </ol>
      </nav>
    </RailSection>
  );

  return (
    <div className="min-h-dvh bg-bg pl-[var(--gutter-l)] pr-[var(--gutter-r)] lg:px-0">
      <PhoneBar back={{ to: "/more", label: "我的账号" }} title={doc.title} />

      {/* 页面主容器：提供居中对齐与左右两侧舒适的呼吸空间 */}
      <main className="mx-auto max-w-[var(--page-max-reading)] px-2 sm:px-6 lg:px-12">
        {/* 桌面端返回链接 */}
        <div className="hidden pt-6 lg:block">
          <BackLink to="/more" label="我的账号" />
        </div>

        <ArticleLayout
          left={
            <>
              {aside}
              {info}
            </>
          }
          right={
            <>
              <div className="space-y-8 2xl:hidden">{info}</div>
              {outline}
            </>
          }
        >
          <article className="pb-16 pt-5 sm:pt-8 lg:pt-6">
            {eyebrow && (
              <div className="mb-2.5 text-[12px] font-semibold uppercase tracking-wider text-accent">
                {eyebrow}
              </div>
            )}
            <h1
              data-page-title=""
              className="text-[26px] font-bold leading-[1.35] tracking-tight text-ink sm:text-[32px] lg:text-[36px]"
            >
              {doc.title}
            </h1>

            {doc.intro && (
              <div className="mt-5 rounded-tile border border-line-soft bg-surface/50 p-4 text-[14px] leading-relaxed text-ink-2 sm:p-5 sm:text-[15px]">
                {doc.intro}
              </div>
            )}

            {facts.length > 0 && (
              <div className="mt-6 rounded-tile border border-line bg-surface/70 p-4 sm:p-5 lg:hidden shadow-xs">
                <div className="mb-3 text-[11.5px] font-semibold uppercase tracking-wider text-ink-4">
                  文档信息
                </div>
                <dl className="grid grid-cols-1 gap-2.5 text-[12.5px] sm:grid-cols-2">
                  {facts.map((k) => (
                    <div
                      key={k}
                      className="flex items-center justify-between border-b border-line-soft/60 pb-2 last:border-b-0 sm:border-b-0 sm:pb-0"
                    >
                      <dt className="text-ink-4">{k}</dt>
                      <dd className="font-medium text-ink-2">{doc.meta[k]}</dd>
                    </div>
                  ))}
                </dl>
              </div>
            )}

            <div
              className="prose prose-compact mt-8 sm:mt-10"
              dangerouslySetInnerHTML={{ __html: rendered.html }}
            />

            {footer && (
              <footer className="mt-14 border-t border-line pt-6 text-[13px] text-ink-3">
                {footer}
              </footer>
            )}
          </article>
        </ArticleLayout>
      </main>
    </div>
  );
}

/** Render footer navigation links and version note for legal pages. */
export function LegalFooterLinks({
  links,
  note,
}: {
  links: Array<{ to: string; label: string; id?: string }>;
  note?: string;
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
      {links.map((l) => (
        <Link key={l.to} id={l.id} to={l.to} className="text-accent hover:underline">
          {l.label}
        </Link>
      ))}
      {note && <span className="text-ink-4">{note}</span>}
    </div>
  );
}
