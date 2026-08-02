import { lazy, type ComponentType, type LazyExoticComponent } from "react";
import { slugify } from "./mdx-components";

type MDXModule = {
  default: ComponentType;
};

const compiledModules = import.meta.glob<MDXModule>(
  ["../plans/open/*.mdx", "../plans/closed/*.mdx"],
);

const rawModules = import.meta.glob<string>(
  ["../plans/open/*.mdx", "../plans/closed/*.mdx"],
  {
    eager: true,
    import: "default",
    query: "?raw",
  },
);

export type DocumentHeading = {
  depth: number;
  title: string;
  id: string;
};

export type Roadmap =
  | { number: string; status: "future" }
  | { completedOn?: string; number: string; status: "completed" };

export type ReaderDocument = {
  path: string;
  route: string;
  title: string;
  /** Title with any leading roadmap number ("01 — ") stripped, for compact nav labels. */
  navTitle: string;
  roadmap: Roadmap;
  description: string;
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
  return path.replace(/\.(md|mdx)$/, "");
}

function roadmapFor(path: string, source: string): Roadmap {
  const number = path.match(/^plans\/(?:open|closed)\/(\d+)-/)?.[1];
  if (!number) {
    throw new Error(`${path} does not follow the numbered roadmap filename convention`);
  }

  if (path.startsWith("plans/open/")) return { number, status: "future" };

  const completedOn = source.match(/\{\/\* Completed (\d{4}-\d{2}-\d{2})\./)?.[1];
  if (!completedOn) {
    // A missing note should cost a date in the banner, not the whole site.
    console.warn(`${path} is closed but has no completion date`);
  }

  return { completedOn, number, status: "completed" };
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

function buildDocument(
  path: string,
  source: string,
  Component: ReaderDocument["Component"],
): ReaderDocument {
  const title = titleFor(path, source);
  const description = descriptionFor(source);

  return {
    path,
    route: routeFor(path),
    title,
    navTitle: title.replace(/^\d+\s+[—-]\s+/, ""),
    roadmap: roadmapFor(path, source),
    description,
    order: orderFor(path),
    readingMinutes: readingMinutesFor(source),
    headings: headingsFor(source),
    source,
    searchText: `${title} ${description} ${source}`.toLowerCase(),
    Component,
  };
}

const mdxDocuments: ReaderDocument[] = Object.entries(compiledModules).map(
  ([modulePath, loadModule]) =>
    buildDocument(filePath(modulePath), rawModules[modulePath] ?? "", lazy(loadModule)),
);

export const documents = mdxDocuments.sort((a, b) => {
  if (a.order !== b.order) return a.order - b.order;
  return a.title.localeCompare(b.title, undefined, { numeric: true });
});

export function resolveDocument(route: string | null): ReaderDocument {
  const normalized = (route ?? "").replace(/^\/|\/$/g, "").replace(/\.(md|mdx)$/, "");
  return documents.find((document) => document.route === normalized) ?? documents[0];
}
