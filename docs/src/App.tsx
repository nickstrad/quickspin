import { MDXProvider } from "@mdx-js/react";
import {
  memo,
  type MouseEvent as ReactMouseEvent,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { ArrowIcon, CloseIcon, FileIcon, MenuIcon, SearchIcon } from "./icons";
import {
  documents,
  resolveDocument,
  sections,
  type DocumentSection,
  type ReaderDocument,
} from "./documents";
import {
  DocumentRouteContext,
  mdxComponents,
  onTaskProgress,
  readTaskStore,
} from "./mdx-components";

// Display label shown above each document. Sections without an entry fall back to
// their own name.
const categoryLabels: Partial<Record<DocumentSection, string>> = {
  "Open plans": "Implementation plan",
  "Closed plans": "Completed plan",
};

function currentRoute(): string | null {
  return new URLSearchParams(window.location.search).get("doc");
}

function hrefFor(document: ReaderDocument): string {
  if (!document.route) return window.location.pathname;
  return `${window.location.pathname}?doc=${encodeURIComponent(document.route)}`;
}

function resolveRelativeDocument(current: ReaderDocument, href: string) {
  const [rawPath] = href.split("#");
  if (!rawPath || rawPath.startsWith("/") || rawPath.includes("://")) return undefined;

  const parts = current.path.split("/");
  parts.pop();

  for (const segment of decodeURIComponent(rawPath).split("/")) {
    if (!segment || segment === ".") continue;
    if (segment === "..") parts.pop();
    else parts.push(segment);
  }

  const route = parts.join("/").replace(/\.(md|mdx)$/, "");
  return documents.find((document) => document.route === route);
}

// Re-render subscribers whenever any plan's task checkboxes change.
function useTaskProgressVersion(): number {
  const [version, setVersion] = useState(0);
  useEffect(() => onTaskProgress(() => setVersion((value) => value + 1)), []);
  return version;
}

function TaskProgressBadge({ document }: { document: ReaderDocument }) {
  const store = readTaskStore(document.route);
  if (!store || store.total === 0) return null;

  const doneCount = Object.keys(store.done).filter((id) => store.done[id]).length;
  const complete = doneCount >= store.total;

  return (
    <span
      className={`nav-item__progress ${complete ? "nav-item__progress--complete" : ""}`}
      title={`${doneCount} of ${store.total} tasks complete`}
    >
      {complete ? "✓" : `${doneCount}/${store.total}`}
    </span>
  );
}

const Sidebar = memo(function Sidebar({
  active,
  query,
  setQuery,
  onNavigate,
  open,
  onClose,
  searchRef,
}: {
  active: ReaderDocument;
  query: string;
  setQuery: (query: string) => void;
  onNavigate: (document: ReaderDocument) => void;
  open: boolean;
  onClose: () => void;
  searchRef: React.RefObject<HTMLInputElement | null>;
}) {
  useTaskProgressVersion();

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return documents;

    return documents.filter((document) => document.searchText.includes(needle));
  }, [query]);

  return (
    <>
      <button
        className={`sidebar-scrim ${open ? "sidebar-scrim--visible" : ""}`}
        onClick={onClose}
        aria-label="Close navigation"
      />
      <aside className={`sidebar ${open ? "sidebar--open" : ""}`}>
        <header className="brand">
          <button className="brand__mark" onClick={() => onNavigate(resolveDocument(""))}>
            <span>Q</span>
          </button>
          <div>
            <p>Quickspin</p>
            <span>Field notes / 01</span>
          </div>
          <button className="sidebar__close" onClick={onClose} aria-label="Close navigation">
            <CloseIcon />
          </button>
        </header>

        <label className="search">
          <SearchIcon />
          <input
            ref={searchRef}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search the field notes"
            aria-label="Search documentation"
          />
          <kbd>/</kbd>
        </label>

        <nav className="document-nav" aria-label="Documentation">
          {sections.map((section) => {
            const sectionDocuments = filtered.filter((document) => document.section === section);
            if (!sectionDocuments.length) return null;

            return (
              <section key={section} className="nav-section">
                <div className="nav-section__heading">
                  <span>{section}</span>
                  <small>{sectionDocuments.length.toString().padStart(2, "0")}</small>
                </div>
                {sectionDocuments.map((document) => (
                  <button
                    key={document.path}
                    className={`nav-item ${
                      document.path === active.path ? "nav-item--active" : ""
                    }`}
                    onClick={() => onNavigate(document)}
                  >
                    <span className="nav-item__number">
                      {document.planNumber ?? <FileIcon />}
                    </span>
                    <span>{document.navTitle}</span>
                    <TaskProgressBadge document={document} />
                  </button>
                ))}
              </section>
            );
          })}
          {filtered.length === 0 ? (
            <div className="search-empty">
              <span>Nothing indexed under</span>
              <strong>“{query}”</strong>
            </div>
          ) : null}
        </nav>

        <footer className="sidebar__footer">
          <span>MDX reader</span>
          <span className="pulse-dot" />
          <span>local</span>
        </footer>
      </aside>
    </>
  );
});

function App() {
  const [active, setActive] = useState(() => resolveDocument(currentRoute()));
  const [query, setQuery] = useState("");
  const [menuOpen, setMenuOpen] = useState(false);
  const [readingProgress, setReadingProgress] = useState(0);
  const searchRef = useRef<HTMLInputElement>(null);

  const activeIndex = documents.findIndex((document) => document.path === active.path);
  const previous = activeIndex > 0 ? documents[activeIndex - 1] : undefined;
  const next = activeIndex < documents.length - 1 ? documents[activeIndex + 1] : undefined;

  const navigate = useCallback((document: ReaderDocument, replace = false) => {
    const method = replace ? "replaceState" : "pushState";
    window.history[method]({}, "", hrefFor(document));
    setActive(document);
    setMenuOpen(false);
    window.scrollTo({ top: 0, behavior: "smooth" });
  }, []);

  const closeMenu = useCallback(() => setMenuOpen(false), []);

  useEffect(() => {
    const handlePopState = () => setActive(resolveDocument(currentRoute()));
    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, []);

  useEffect(() => {
    document.title = `${active.title} · Quickspin`;
  }, [active]);

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (
        event.key === "/" &&
        document.activeElement?.tagName !== "INPUT" &&
        document.activeElement?.tagName !== "TEXTAREA"
      ) {
        event.preventDefault();
        searchRef.current?.focus();
      }
      if (event.key === "Escape") {
        setQuery("");
        setMenuOpen(false);
        searchRef.current?.blur();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, []);

  useEffect(() => {
    const updateProgress = () => {
      const scrollable = document.documentElement.scrollHeight - window.innerHeight;
      setReadingProgress(scrollable > 0 ? (window.scrollY / scrollable) * 100 : 0);
    };
    updateProgress();
    window.addEventListener("scroll", updateProgress, { passive: true });
    window.addEventListener("resize", updateProgress);
    return () => {
      window.removeEventListener("scroll", updateProgress);
      window.removeEventListener("resize", updateProgress);
    };
  }, [active]);

  const handleDocumentClick = (event: ReactMouseEvent<HTMLElement>) => {
    const target = event.target as HTMLElement;
    const anchor = target.closest("a");
    const href = anchor?.getAttribute("href");
    if (!href) return;

    if (href.startsWith("#")) {
      event.preventDefault();
      document.getElementById(href.slice(1))?.scrollIntoView({ behavior: "smooth" });
      window.history.replaceState({}, "", `${hrefFor(active)}${href}`);
      return;
    }

    const targetDocument = resolveRelativeDocument(active, href);
    if (targetDocument) {
      event.preventDefault();
      navigate(targetDocument);
    }
  };

  const ActiveDocument = active.Component;
  const categoryLabel = categoryLabels[active.section] ?? active.section;

  return (
    <div className="app-shell">
      <div className="reading-progress" style={{ width: `${readingProgress}%` }} />

      <Sidebar
        active={active}
        query={query}
        setQuery={setQuery}
        onNavigate={navigate}
        open={menuOpen}
        onClose={closeMenu}
        searchRef={searchRef}
      />

      <header className="mobile-header">
        <button onClick={() => setMenuOpen(true)} aria-label="Open navigation">
          <MenuIcon />
        </button>
        <span>Quickspin / field notes</span>
        <span className="mobile-header__page">{String(activeIndex + 1).padStart(2, "0")}</span>
      </header>

      <main className="reader">
        <article className="paper">
          <div className="document-meta">
            <span>{categoryLabel}</span>
            <span>{active.readingMinutes} min read</span>
            <span>{active.path}</span>
          </div>

          <div className="document-body" onClick={handleDocumentClick}>
            <DocumentRouteContext.Provider value={active.route}>
              <MDXProvider components={mdxComponents}>
              <Suspense
                fallback={
                  <div className="document-loading">
                    <span />
                    Preparing field notes…
                  </div>
                }
              >
                  <ActiveDocument />
                </Suspense>
              </MDXProvider>
            </DocumentRouteContext.Provider>
          </div>

          <nav className="page-turner" aria-label="Previous and next document">
            {previous ? (
              <button className="page-turner__previous" onClick={() => navigate(previous)}>
                <ArrowIcon />
                <span>
                  <small>Previous</small>
                  {previous.title}
                </span>
              </button>
            ) : (
              <span />
            )}
            {next ? (
              <button className="page-turner__next" onClick={() => navigate(next)}>
                <span>
                  <small>Next</small>
                  {next.title}
                </span>
                <ArrowIcon />
              </button>
            ) : null}
          </nav>
        </article>

        <aside className="outline">
          <p>On this page</p>
          <nav>
            {active.headings.length ? (
              active.headings.map((heading) => (
                <button
                  key={`${heading.id}-${heading.depth}`}
                  className={heading.depth === 3 ? "outline__nested" : ""}
                  onClick={() =>
                    document
                      .getElementById(heading.id)
                      ?.scrollIntoView({ behavior: "smooth" })
                  }
                >
                  {heading.title}
                </button>
              ))
            ) : (
              <span>No sections indexed.</span>
            )}
          </nav>
          <div className="outline__rule" />
          <p className="outline__folio">
            QS / {String(activeIndex + 1).padStart(2, "0")}
          </p>
        </aside>
      </main>
    </div>
  );
}

export default App;
