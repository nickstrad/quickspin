import { lazy, type ComponentType, type LazyExoticComponent } from "react";
import ReactMarkdown from "react-markdown";
import rehypeHighlight from "rehype-highlight";
import rehypeSlug from "rehype-slug";
import remarkGfm from "remark-gfm";
import type { Components } from "react-markdown";
import { mdxComponents, slugify } from "./mdx-components";

type MDXModule = {
  default: ComponentType;
};

const compiledModules = import.meta.glob<MDXModule>(
  ["../*.mdx", "../plans/open/*.mdx", "../plans/closed/*.mdx", "../reference/*.mdx"],
);

const rawModules = import.meta.glob<string>(
  ["../*.mdx", "../plans/open/*.mdx", "../plans/closed/*.mdx", "../reference/*.mdx"],
  {
    eager: true,
    import: "default",
    query: "?raw",
  },
);

const instructionSource = import.meta.glob<string>("../plans/AGENTS.md", {
  eager: true,
  import: "default",
  query: "?raw",
});

export type DocumentSection = "Start here" | "Open plans" | "Closed plans" | "Reference";

export type DocumentHeading = {
  depth: number;
  title: string;
  id: string;
};

export type ReaderDocument = {
  path: string;
  route: string;
  title: string;
  /** Title with any leading plan number ("01 — ") stripped, for compact nav labels. */
  navTitle: string;
  /** The leading plan number from the file name, if any (e.g. "03"). */
  planNumber?: string;
  description: string;
  section: DocumentSection;
  order: number;
  readingMinutes: number;
  headings: DocumentHeading[];
  source: string;
  /** Lowercased title + description + source, precomputed once for search filtering. */
  searchText: string;
  Component: ComponentType | LazyExoticComponent<ComponentType>;
};

function filePath(modulePath: string): string {
  return modulePath.replace(/^\.\.\//, "");
}

function routeFor(path: string): string {
  if (path === "index.mdx") {
    return "";
  }

  return path.replace(/\.(md|mdx)$/, "");
}

function sectionFor(path: string): DocumentSection {
  if (path.startsWith("plans/open/")) return "Open plans";
  if (path.startsWith("plans/closed/")) return "Closed plans";
  if (path.startsWith("reference/")) return "Reference";
  return "Start here";
}

function titleFor(path: string, source: string): string {
  const title = source.match(/^#\s+(.+)$/m)?.[1];
  if (title) return title.replace(/[*_`]/g, "").trim();

  return path
    .split("/")
    .at(-1)!
    .replace(/\.(md|mdx)$/, "")
    .replace(/[-_]/g, " ");
}

function descriptionFor(source: string): string {
  const withoutCode = source.replace(/```[\s\S]*?```/g, "");
  const paragraphs = withoutCode.split(/\n\s*\n/);
  const description = paragraphs.find((paragraph) => {
    const text = paragraph.trim();
    return (
      text.length > 60 &&
      !text.startsWith("#") &&
      !text.startsWith("Depends on:") &&
      !text.startsWith("<") &&
      !text.startsWith("- ")
    );
  });

  return (description ?? "Quickspin project documentation.")
    .replace(/\[([^\]]+)\]\([^)]+\)/g, "$1")
    .replace(/[*_`>#]/g, "")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 190);
}

function headingsFor(source: string): DocumentHeading[] {
  return source
    .split("\n")
    .flatMap((line) => {
      const match = /^(#{2,3})\s+(.+)$/.exec(line);
      if (!match) return [];

      const title = match[2]
        .replace(/\[([^\]]+)\]\([^)]+\)/g, "$1")
        .replace(/[*_`]/g, "")
        .trim();

      return [
        {
          depth: match[1].length,
          title,
          id: slugify(title),
        },
      ];
    });
}

function readingMinutesFor(source: string): number {
  const words = source
    .replace(/```[\s\S]*?```/g, "")
    .replace(/<[^>]+>/g, "")
    .trim()
    .split(/\s+/).length;
  return Math.max(1, Math.ceil(words / 220));
}

function orderFor(path: string): number {
  const fileName = path.split("/").at(-1) ?? "";
  return Number(fileName.match(/^(\d+)/)?.[1] ?? 999);
}

// Builds the full ReaderDocument from a file's raw source. The only per-document
// differences are how the component is produced and, for the agent doc, its fixed
// placement — everything else is derived uniformly here so new fields cannot drift.
function buildDocument(
  path: string,
  source: string,
  Component: ReaderDocument["Component"],
  overrides?: Partial<Pick<ReaderDocument, "section" | "order">>,
): ReaderDocument {
  const title = titleFor(path, source);
  const description = descriptionFor(source);

  return {
    path,
    route: routeFor(path),
    title,
    navTitle: title.replace(/^\d+\s+[—-]\s+/, ""),
    planNumber: path.match(/\/(\d+)-/)?.[1],
    description,
    section: sectionFor(path),
    order: orderFor(path),
    readingMinutes: readingMinutesFor(source),
    headings: headingsFor(source),
    source,
    searchText: `${title} ${description} ${source}`.toLowerCase(),
    Component,
    ...overrides,
  };
}

const mdxDocuments: ReaderDocument[] = Object.entries(compiledModules).map(
  ([modulePath, loadModule]) =>
    buildDocument(filePath(modulePath), rawModules[modulePath] ?? "", lazy(loadModule)),
);

const agentEntry = Object.entries(instructionSource)[0];
const agentDocument: ReaderDocument[] = agentEntry
  ? (() => {
      const [modulePath, source] = agentEntry;
      const MarkdownInstructions = () => (
        <ReactMarkdown
          remarkPlugins={[remarkGfm]}
          rehypePlugins={[rehypeSlug, rehypeHighlight]}
          components={mdxComponents as Components}
        >
          {source}
        </ReactMarkdown>
      );

      return [
        buildDocument(filePath(modulePath), source, MarkdownInstructions, {
          section: "Start here",
          order: 2,
        }),
      ];
    })()
  : [];

const sectionOrder: Record<DocumentSection, number> = {
  "Start here": 0,
  "Open plans": 1,
  "Closed plans": 2,
  Reference: 3,
};

// The single source of truth for section identity and display order. The sidebar
// renders sections in this order, so it must not maintain its own copy.
export const sections = (Object.keys(sectionOrder) as DocumentSection[]).sort(
  (a, b) => sectionOrder[a] - sectionOrder[b],
);

export const documents = [...mdxDocuments, ...agentDocument].sort((a, b) => {
  const sectionDifference = sectionOrder[a.section] - sectionOrder[b.section];
  if (sectionDifference !== 0) return sectionDifference;
  if (a.order !== b.order) return a.order - b.order;
  return a.title.localeCompare(b.title, undefined, { numeric: true });
});

export function resolveDocument(route: string | null): ReaderDocument {
  const normalized = (route ?? "").replace(/^\/|\/$/g, "").replace(/\.(md|mdx)$/, "");
  return (
    documents.find((document) => document.route === normalized) ??
    documents.find((document) => document.route === "") ??
    documents[0]
  );
}
