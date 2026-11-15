import type { ReactNode } from "react";

interface InlinePart {
  type: "text" | "bold" | "italic" | "code" | "link";
  content?: string;
  url?: string;
}

interface MarkdownBlock {
  type: "heading" | "ul" | "ol" | "paragraph" | "quote";
  level?: number;
  lines?: string[];
  items?: string[];
  text?: string;
}

/** Parses inline markdown syntax into typed parts. */
function parseInlineParts(text: string): InlinePart[] {
  // 匹配加粗、行内代码、Markdown 链接与斜体
  const regex = /(\*\*([^*]+)\*\*|`([^`]+)`|\[([^\]]+)\]\(([^)]+)\)|\*([^*]+)\*)/g;
  const parts: InlinePart[] = [];
  let lastIndex = 0;
  let match: RegExpExecArray | null;

  while ((match = regex.exec(text)) !== null) {
    if (match.index > lastIndex) {
      parts.push({
        type: "text",
        content: text.slice(lastIndex, match.index),
      });
    }

    if (match[2]) {
      parts.push({
        type: "bold",
        content: match[2],
      });
    } else if (match[3]) {
      parts.push({
        type: "code",
        content: match[3],
      });
    } else if (match[4] && match[5]) {
      parts.push({
        type: "link",
        content: match[4],
        url: match[5],
      });
    } else if (match[6]) {
      parts.push({
        type: "italic",
        content: match[6],
      });
    }

    lastIndex = regex.lastIndex;
  }

  if (lastIndex < text.length) {
    parts.push({
      type: "text",
      content: text.slice(lastIndex),
    });
  }

  return parts;
}

/** Renders inline text with rich elements. */
function renderInline(text: string): ReactNode {
  const parts = parseInlineParts(text);

  if (parts.length === 0) {
    return text;
  }

  return parts.map((part, index) => {
    if (part.type === "bold") {
      return (
        <strong key={index} className="font-bold text-ink">
          {part.content}
        </strong>
      );
    }

    if (part.type === "italic") {
      return (
        <em key={index} className="italic">
          {part.content}
        </em>
      );
    }

    if (part.type === "code") {
      return (
        <code
          key={index}
          className="rounded bg-bg-sunk px-1.5 py-0.5 font-mono text-[0.88em] text-accent"
        >
          {part.content}
        </code>
      );
    }

    if (part.type === "link") {
      return (
        <a
          key={index}
          href={part.url}
          target="_blank"
          rel="noopener noreferrer"
          className="text-accent underline underline-offset-2 hover:text-accent-ink"
        >
          {part.content}
        </a>
      );
    }

    return part.content;
  });
}

/** Parses markdown text into structured blocks. */
function parseBlocks(markdown: string): MarkdownBlock[] {
  const lines = markdown.replace(/\r\n/g, "\n").split("\n");
  const blocks: MarkdownBlock[] = [];
  let currentList: { type: "ul" | "ol"; items: string[] } | null = null;
  let currentParagraph: string[] | null = null;

  /** 结算当前累积的列表块 */
  function flushList() {
    if (currentList) {
      blocks.push({
        type: currentList.type,
        items: currentList.items,
      });
      currentList = null;
    }
  }

  /** 结算当前累积的段落块 */
  function flushParagraph() {
    if (currentParagraph && currentParagraph.length > 0) {
      blocks.push({
        type: "paragraph",
        lines: currentParagraph,
      });
      currentParagraph = null;
    }
  }

  for (let i = 0; i < lines.length; i++) {
    const rawLine = lines[i]!;
    const line = rawLine.trim();

    // 空行作为自然分块分隔符
    if (!line) {
      flushList();
      flushParagraph();
      continue;
    }

    // 无序列表项：- / * / •
    const bulletMatch = line.match(/^[-*•]\s+(.*)$/);
    if (bulletMatch) {
      flushParagraph();
      if (!currentList || currentList.type !== "ul") {
        flushList();
        currentList = {
          type: "ul",
          items: [],
        };
      }
      currentList.items.push(bulletMatch[1] ?? "");
      continue;
    }

    // 有序列表项：1. / 1、 / 1．
    const orderedMatch = line.match(/^\d+[.、．]\s+(.*)$/);
    if (orderedMatch) {
      flushParagraph();
      if (!currentList || currentList.type !== "ol") {
        flushList();
        currentList = {
          type: "ol",
          items: [],
        };
      }
      currentList.items.push(orderedMatch[1] ?? "");
      continue;
    }

    flushList();

    // 块级独立加粗小标题：**标题** 或 **标题**：
    const boldHeaderMatch = line.match(/^\*\*([^*]+)\*\*[:：]?$/);
    if (boldHeaderMatch) {
      flushParagraph();
      blocks.push({
        type: "heading",
        level: 4,
        text: boldHeaderMatch[1],
      });
      continue;
    }

    // 标准 Markdown 标题：### 标题
    const hashHeaderMatch = line.match(/^(#{1,6})\s+(.*)$/);
    if (hashHeaderMatch) {
      flushParagraph();
      blocks.push({
        type: "heading",
        level: (hashHeaderMatch[1] ?? "###").length,
        text: hashHeaderMatch[2],
      });
      continue;
    }

    // 引用块：> 引用内容
    const quoteMatch = line.match(/^>\s?(.*)$/);
    if (quoteMatch) {
      flushParagraph();
      blocks.push({
        type: "quote",
        text: quoteMatch[1],
      });
      continue;
    }

    // 常规段落行收集
    if (!currentParagraph) {
      currentParagraph = [];
    }
    currentParagraph.push(line);
  }

  flushList();
  flushParagraph();
  return blocks;
}

/** Renders markdown content safely as styled React elements without innerHTML. */
export function MarkdownContent({
  content,
  className = "",
}: {
  content: string;
  className?: string;
}) {
  if (!content) {
    return null;
  }

  // 解析 Markdown 为结构化渲染块
  const blocks = parseBlocks(content);

  return (
    <div className={`space-y-3 ${className}`}>
      {blocks.map((block, index) => {
        if (block.type === "heading") {
          return (
            <h4
              key={index}
              className="mt-6 mb-2 text-[16px] font-bold tracking-tight text-ink first:mt-1"
            >
              {renderInline(block.text ?? "")}
            </h4>
          );
        }

        if (block.type === "ul") {
          return (
            <ul
              key={index}
              className="my-2.5 list-disc space-y-2 pl-5 text-[15px] leading-[1.85] text-ink-2"
            >
              {block.items?.map((item, itemIdx) => (
                <li key={itemIdx} className="marker:text-ink-4">
                  {renderInline(item)}
                </li>
              ))}
            </ul>
          );
        }

        if (block.type === "ol") {
          return (
            <ol
              key={index}
              className="my-2.5 list-decimal space-y-2 pl-5 text-[15px] leading-[1.85] text-ink-2"
            >
              {block.items?.map((item, itemIdx) => (
                <li key={itemIdx} className="marker:text-ink-4">
                  {renderInline(item)}
                </li>
              ))}
            </ol>
          );
        }

        if (block.type === "quote") {
          return (
            <blockquote
              key={index}
              className="my-3 border-l-2 border-accent/40 pl-3.5 text-[14px] italic text-ink-3"
            >
              {renderInline(block.text ?? "")}
            </blockquote>
          );
        }

        return (
          <p key={index} className="text-[15px] leading-[1.85] text-ink-2">
            {block.lines?.map((line, lineIdx) => (
              <span key={lineIdx}>
                {lineIdx > 0 && <br />}
                {renderInline(line)}
              </span>
            ))}
          </p>
        );
      })}
    </div>
  );
}
