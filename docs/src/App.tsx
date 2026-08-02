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
import {
  ArrowIcon,
  CheckIcon,
  ChevronIcon,
  CloseIcon,
  MenuIcon,
  ReaderModeIcon,
  SearchIcon,
  SettingsIcon,
} from "./icons";
import {
  documents,
  resolveDocument,
  type ReaderDocument,
  type Roadmap,
} from "./documents";
import { mdxComponents } from "./mdx-components";

const completionDateFormatter = new Intl.DateTimeFormat("en-US", {
  dateStyle: "long",
  timeZone: "UTC",
});

type ReaderFontSize = "xs" | "sm" | "md" | "lg" | "xl";

type ReaderPreferences = {
  fontSize: ReaderFontSize;
  readerMode: boolean;
  sidebarCollapsed: boolean;
};

const defaultReaderPreferences: ReaderPreferences = {
  fontSize: "md",
  readerMode: false,
  sidebarCollapsed: false,
};

const fontSizeOptions: { label: string; shortLabel: string; value: ReaderFontSize }[] = [
  { label: "Very small", shortLabel: "XS", value: "xs" },
  { label: "Small", shortLabel: "S", value: "sm" },
  { label: "Default", shortLabel: "M", value: "md" },
  { label: "Large", shortLabel: "L", value: "lg" },
  { label: "Very large", shortLabel: "XL", value: "xl" },
];

const readerPreferencesKey = "quickspin-reader-preferences";

function loadReaderPreferences(): ReaderPreferences {
  try {
    const saved = window.localStorage.getItem(readerPreferencesKey);
    if (!saved) return defaultReaderPreferences;

    const parsed = JSON.parse(saved) as Partial<ReaderPreferences>;
    const fontSize = fontSizeOptions.some((option) => option.value === parsed.fontSize)
      ? parsed.fontSize!
      : defaultReaderPreferences.fontSize;

    return {
      fontSize,
      readerMode: parsed.readerMode === true,
      sidebarCollapsed: parsed.sidebarCollapsed === true,
    };
  } catch {
    return defaultReaderPreferences;
  }
}

function RoadmapStatus({ roadmap }: { roadmap: Roadmap }) {
  const completed = roadmap.status === "completed";
  const completedOn =
    completed && roadmap.completedOn
      ? completionDateFormatter.format(new Date(`${roadmap.completedOn}T00:00:00Z`))
      : undefined;

  return (
    <div className={`roadmap-status roadmap-status--${roadmap.status}`}>
      <span className="roadmap-status__mark">
        {completed ? <CheckIcon /> : roadmap.number}
      </span>
      <div>
        <strong>{completed ? "Delivered" : "Future state"}</strong>
        <p>
          {completed
            ? `${completedOn ? `Marked complete ${completedOn}.` : "Marked complete."} This roadmap is retained as historical context; current code and tests are authoritative where delivery differs.`
            : "This roadmap outlines Quickspin’s intended direction, not its current behavior. Use the codebase as the authority for what exists today."}
        </p>
      </div>
    </div>
  );
}

function currentRoute(): string | null {
  return new URLSearchParams(window.location.search).get("doc");
}

function hrefFor(document: ReaderDocument): string {
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

const Sidebar = memo(function Sidebar({
  active,
  query,
  setQuery,
  onNavigate,
  open,
  onClose,
  collapsed,
  collapseLocked,
  onToggleCollapsed,
  searchRef,
}: {
  active: ReaderDocument;
  query: string;
  setQuery: (query: string) => void;
  onNavigate: (document: ReaderDocument) => void;
  open: boolean;
  onClose: () => void;
  collapsed: boolean;
  collapseLocked: boolean;
  onToggleCollapsed: () => void;
  searchRef: React.RefObject<HTMLInputElement | null>;
}) {
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
      <aside
        id="documentation-sidebar"
        className={`sidebar ${open ? "sidebar--open" : ""} ${
          collapsed ? "sidebar--collapsed" : ""
        }`}
      >
        <header className="brand">
          <button
            className="brand__mark"
            onClick={() => onNavigate(resolveDocument(""))}
            aria-label="Quickspin documentation home"
            title={collapsed ? "Quickspin home" : undefined}
          >
            <span>Q</span>
          </button>
          <div>
            <p>Quickspin</p>
            <span>Platform roadmap</span>
          </div>
          <button className="sidebar__close" onClick={onClose} aria-label="Close navigation">
            <CloseIcon />
          </button>
        </header>

        {!collapseLocked ? (
          <button
            className="sidebar__collapse"
            onClick={onToggleCollapsed}
            aria-controls="documentation-sidebar"
            aria-expanded={!collapsed}
            aria-label={collapsed ? "Expand navigation" : "Collapse navigation"}
            title={collapsed ? "Expand navigation" : "Collapse navigation"}
          >
            <ChevronIcon />
          </button>
        ) : null}

        <label className="search">
          <SearchIcon />
          <input
            ref={searchRef}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search the roadmap"
            aria-label="Search documentation"
          />
          <kbd>/</kbd>
        </label>

        <nav id="document-navigation" className="document-nav" aria-label="Roadmap">
          {filtered.length ? (
            <section className="nav-section">
              <div className="nav-section__heading">
                <span>Roadmap</span>
                <small>{filtered.length.toString().padStart(2, "0")}</small>
              </div>
              {filtered.map((document) => (
                <button
                  key={document.path}
                  className={`nav-item ${
                    document.path === active.path ? "nav-item--active" : ""
                  }`}
                  onClick={() => onNavigate(document)}
                  aria-current={document.path === active.path ? "page" : undefined}
                  title={collapsed ? document.navTitle : undefined}
                >
                  <span className="nav-item__number">{document.roadmap.number}</span>
                  <span className="nav-item__label">{document.navTitle}</span>
                  {document.roadmap.status === "completed" ? (
                    <span className="nav-item__complete" title="Completed roadmap">
                      <CheckIcon />
                      <span className="sr-only">Completed roadmap</span>
                    </span>
                  ) : null}
                </button>
              ))}
            </section>
          ) : null}
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
  const [preferences, setPreferences] = useState(loadReaderPreferences);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const readerControlsRef = useRef<HTMLDivElement>(null);

  const sidebarCollapsed = preferences.sidebarCollapsed || preferences.readerMode;

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
    try {
      window.localStorage.setItem(readerPreferencesKey, JSON.stringify(preferences));
    } catch {
      // Preferences remain available for the current session when storage is unavailable.
    }
  }, [preferences]);

  useEffect(() => {
    if (!settingsOpen) return;

    const handlePointerDown = (event: PointerEvent) => {
      if (!readerControlsRef.current?.contains(event.target as Node)) {
        setSettingsOpen(false);
      }
    };

    window.addEventListener("pointerdown", handlePointerDown);
    return () => window.removeEventListener("pointerdown", handlePointerDown);
  }, [settingsOpen]);

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (
        event.key === "/" &&
        document.activeElement?.tagName !== "INPUT" &&
        document.activeElement?.tagName !== "TEXTAREA"
      ) {
        event.preventDefault();
        setPreferences((current) => ({
          ...current,
          readerMode: false,
          sidebarCollapsed: false,
        }));
        window.requestAnimationFrame(() => searchRef.current?.focus());
      }
      if (event.key === "Escape") {
        setQuery("");
        setMenuOpen(false);
        setSettingsOpen(false);
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
  const categoryLabel =
    active.roadmap.status === "completed" ? "Completed roadmap" : "Roadmap";

  return (
    <div
      className={`app-shell reader-font--${preferences.fontSize} ${
        sidebarCollapsed ? "app-shell--sidebar-collapsed" : ""
      } ${preferences.readerMode ? "app-shell--reader-mode" : ""}`}
    >
      <div className="reading-progress" style={{ width: `${readingProgress}%` }} />

      <Sidebar
        active={active}
        query={query}
        setQuery={setQuery}
        onNavigate={navigate}
        open={menuOpen}
        onClose={closeMenu}
        collapsed={sidebarCollapsed}
        collapseLocked={preferences.readerMode}
        onToggleCollapsed={() =>
          setPreferences((current) => ({
            ...current,
            sidebarCollapsed: !current.sidebarCollapsed,
          }))
        }
        searchRef={searchRef}
      />

      <header className="mobile-header">
        <button onClick={() => setMenuOpen(true)} aria-label="Open navigation">
          <MenuIcon />
        </button>
        <span>Quickspin / roadmap</span>
        <span className="mobile-header__page">{String(activeIndex + 1).padStart(2, "0")}</span>
      </header>

      <main className="reader">
        <article className="paper">
          <div className="document-meta">
            <span>{categoryLabel}</span>
            <span>{active.readingMinutes} min read</span>
            <span>{active.path}</span>
          </div>

          <RoadmapStatus roadmap={active.roadmap} />

          <div className="document-body" onClick={handleDocumentClick}>
            <MDXProvider components={mdxComponents}>
              <Suspense
                fallback={
                  <div className="document-loading">
                    <span />
                    Preparing roadmap…
                  </div>
                }
              >
                <ActiveDocument />
              </Suspense>
            </MDXProvider>
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

      <div className="reader-controls" ref={readerControlsRef}>
        {settingsOpen ? (
          <div
            id="reader-settings"
            className="reader-settings"
            role="dialog"
            aria-labelledby="reader-settings-title"
          >
            <div className="reader-settings__header">
              <div>
                <p id="reader-settings-title">Reading settings</p>
                <span>Choose a comfortable text size.</span>
              </div>
              <span className="reader-settings__folio">Aa / 05</span>
            </div>
            <div className="reader-settings__sizes" role="radiogroup" aria-label="Text size">
              {fontSizeOptions.map((option) => (
                <button
                  key={option.value}
                  className={
                    option.value === preferences.fontSize
                      ? "reader-settings__size reader-settings__size--active"
                      : "reader-settings__size"
                  }
                  onClick={() =>
                    setPreferences((current) => ({ ...current, fontSize: option.value }))
                  }
                  role="radio"
                  aria-checked={option.value === preferences.fontSize}
                  aria-label={`${option.label} text`}
                  title={option.label}
                >
                  <span className={`reader-settings__sample reader-settings__sample--${option.value}`}>
                    Aa
                  </span>
                  <small>{option.shortLabel}</small>
                </button>
              ))}
            </div>
            <p className="reader-settings__current" aria-live="polite">
              {fontSizeOptions.find((option) => option.value === preferences.fontSize)?.label}
            </p>
          </div>
        ) : null}

        <div className="reader-controls__dock">
          <button
            className={settingsOpen ? "reader-control reader-control--active" : "reader-control"}
            onClick={() => setSettingsOpen((open) => !open)}
            aria-controls="reader-settings"
            aria-expanded={settingsOpen}
            aria-label="Reading settings"
            title="Reading settings"
          >
            <SettingsIcon />
          </button>
          <span className="reader-controls__divider" />
          <button
            className={
              preferences.readerMode ? "reader-control reader-control--active" : "reader-control"
            }
            onClick={() => {
              setPreferences((current) => ({ ...current, readerMode: !current.readerMode }));
              setSettingsOpen(false);
            }}
            aria-pressed={preferences.readerMode}
            aria-label={preferences.readerMode ? "Exit reader mode" : "Enter reader mode"}
            title={preferences.readerMode ? "Exit reader mode" : "Enter reader mode"}
          >
            <ReaderModeIcon />
          </button>
        </div>
      </div>
    </div>
  );
}

export default App;
