import { useState, type ReactNode } from "react";
import type { MediaView } from "@aihot/contracts/site";
import { Lightbox, SafeMediaImage } from "../../components/ui/Lightbox";

/** A round play mark over a video's still. */
function PlayMark() {
  return (
    <span className="absolute inset-0 grid place-items-center" aria-hidden="true">
      <span className="grid size-11 place-items-center rounded-full bg-black/55 text-white ring-1 ring-white/30 backdrop-blur-sm transition-transform duration-200 group-hover:scale-105">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor" className="ml-0.5">
          <path d="M7 4.5v15a1 1 0 001.5.87l13-7.5a1 1 0 000-1.74l-13-7.5A1 1 0 007 4.5z" />
        </svg>
      </span>
    </span>
  );
}

/**
 * The post's pictures as tiles, all of them (X allows up to nine; list cards show four). Images open in
 * a viewer that steps through them; videos are only a still in our data, so they open the original post.
 */
export function MediaGallery({
  media,
  postUrl,
  header,
}: {
  media: MediaView[];
  postUrl: string;
  header?: ReactNode | ((visibleCount: number) => ReactNode);
}) {
  const [open, setOpen] = useState<number | null>(null);
  const [broken, setBroken] = useState<Record<string, true>>({});
  const shown = media.filter((m) => !broken[m.url]).slice(0, 9);

  if (shown.length === 0) {
    return null;
  }

  const images = shown.filter((m) => m.kind !== "video");
  const single = shown.length === 1;
  const tile = `group relative overflow-hidden rounded-tile border border-line-soft bg-bg-sunk ${single ? "max-w-[460px]" : "aspect-[16/10]"}`;

  /** Remove failed images cleanly if the upstream host stops serving them. */
  const markBroken = (url: string) => {
    setBroken((prev) => {
      if (prev[url]) {
        return prev;
      }

      return {
        ...prev,
        [url]: true,
      };
    });
  };

  return (
    <>
      {typeof header === "function" ? header(shown.length) : header}
      <div className={`mt-5 grid gap-2.5 ${single ? "grid-cols-1" : shown.length === 2 || shown.length === 4 ? "grid-cols-2" : "grid-cols-2 sm:grid-cols-3"}`}>
        {shown.map((m) => {
          const img = (
            <>
              <SafeMediaImage
                src={m.poster ?? m.url}
                srcSet={m.srcSet}
                sizes={single ? "auto, (min-width: 500px) 460px, calc(100vw - 32px)" : "auto, (min-width: 800px) 248px, (min-width: 640px) calc(33.333vw - 19px), calc(50vw - 24px)"}
                width={m.width ?? undefined}
                height={m.height ?? undefined}
                decoding="async"
                alt={m.alt ?? ""}
                loading="lazy"
                onFail={() => markBroken(m.url)}
                className={`block w-full object-cover transition-transform duration-300 group-hover:scale-[1.015] ${single ? "max-h-[360px]" : "h-full"}`}
                style={single && m.width && m.height ? { aspectRatio: `${m.width} / ${m.height}` } : undefined}
              />
              {m.alt && (
                <span className="pointer-events-none absolute inset-x-0 bottom-0 truncate bg-gradient-to-t from-black/70 via-black/35 to-transparent px-2.5 pb-1.5 pt-4 text-left text-[11px] text-white/95">
                  {m.alt}
                </span>
              )}
            </>
          );

          if (m.kind === "video") {
            return (
              <a key={m.url} href={postUrl} target="_blank" rel="noopener noreferrer" aria-label="打开原文播放视频" className={tile}>
                {img}
                <PlayMark />
              </a>
            );
          }

          return (
            <button
              key={m.url}
              type="button"
              onClick={() => setOpen(images.indexOf(m))}
              aria-label={m.alt ? `查看大图：${m.alt}` : "查看大图"}
              className={`${tile} cursor-zoom-in text-left`}
            >
              {img}
            </button>
          );
        })}
      </div>
      <Lightbox
        images={images.map((m) => ({
          src: m.fullUrl ?? m.url,
          alt: m.alt,
        }))}
        index={open}
        onIndex={setOpen}
        onClose={() => setOpen(null)}
      />
    </>
  );
}
