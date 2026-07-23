import type { MDXComponents } from "mdx/types";
import {
  Children,
  createContext,
  isValidElement,
  useContext,
  useEffect,
  useState,
  type AnchorHTMLAttributes,
  type HTMLAttributes,
  type PropsWithChildren,
  type ReactElement,
  type ReactNode,
} from "react";

function childText(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") {
    return String(node);
  }

  if (Array.isArray(node)) {
    return node.map(childText).join("");
  }

  if (node && typeof node === "object" && "props" in node) {
    return childText((node.props as { children?: ReactNode }).children);
  }

  return "";
}

export function slugify(value: string): string {
  return value
    .toLowerCase()
    .replace(/[`*_]/g, "")
    .replace(/[^\p{L}\p{N}]+/gu, "-")
    .replace(/^-+|-+$/g, "");
}

function Heading({
  level,
  children,
  id,
  ...props
}: HTMLAttributes<HTMLHeadingElement> & {
  level: 1 | 2 | 3 | 4;
}) {
  const Component = `h${level}` as "h1" | "h2" | "h3" | "h4";
  const anchor = id ?? slugify(childText(children));

  return (
    <Component id={anchor} {...props}>
      {level > 1 ? (
        <a className="heading-anchor" href={`#${anchor}`} aria-label="Link to this section">
          #
        </a>
      ) : null}
      {children}
    </Component>
  );
}

function SmartLink({ children, ...props }: AnchorHTMLAttributes<HTMLAnchorElement>) {
  const external = props.href?.startsWith("http");

  return (
    <a
      {...props}
      {...(external ? { target: "_blank", rel: "noreferrer" } : {})}
    >
      {children}
      {external ? <span className="external-mark">↗</span> : null}
    </a>
  );
}

type CalloutTone = "note" | "idea" | "warning" | "system";

export function Callout({
  tone = "note",
  title,
  children,
}: PropsWithChildren<{ tone?: CalloutTone; title?: string }>) {
  const labels: Record<CalloutTone, string> = {
    note: "Field note",
    idea: "Working idea",
    warning: "Watch the edge",
    system: "System behavior",
  };

  return (
    <aside className={`callout callout--${tone}`}>
      <div className="callout__label">{title ?? labels[tone]}</div>
      <div className="callout__body">{children}</div>
    </aside>
  );
}

export function StudyPrompt({
  question,
  children,
  label = "Reveal a hint",
}: PropsWithChildren<{ question: string; label?: string }>) {
  return (
    <section className="study-prompt">
      <p className="study-prompt__eyebrow">Pause &amp; reason</p>
      <h3>{question}</h3>
      <details>
        <summary>{label}</summary>
        <div className="study-prompt__answer">{children}</div>
      </details>
    </section>
  );
}

export function KeyPoint({
  label = "Key point",
  children,
}: PropsWithChildren<{ label?: string }>) {
  return (
    <div className="key-point">
      <span>{label}</span>
      <div>{children}</div>
    </div>
  );
}

export function Steps({ children }: PropsWithChildren) {
  return <ol className="study-steps">{children}</ol>;
}

export function Step({
  title,
  children,
}: PropsWithChildren<{ title: string }>) {
  return (
    <li>
      <div className="study-step__marker" aria-hidden="true" />
      <div>
        <strong>{title}</strong>
        <div>{children}</div>
      </div>
    </li>
  );
}

export function Compare({ children }: PropsWithChildren) {
  return <div className="compare-grid">{children}</div>;
}

export function CompareItem({
  title,
  eyebrow,
  children,
}: PropsWithChildren<{ title: string; eyebrow?: string }>) {
  return (
    <section className="compare-card">
      {eyebrow ? <span>{eyebrow}</span> : null}
      <h3>{title}</h3>
      <div>{children}</div>
    </section>
  );
}

// The route of the document currently being rendered. App provides it so
// stateful study components can persist progress per plan.
export const DocumentRouteContext = createContext<string>("");

export type TaskStore = {
  done: Record<string, boolean>;
  total: number;
};

function taskStorageKey(route: string): string {
  return `quickspin.tasks.${route}`;
}

export function readTaskStore(route: string): TaskStore | null {
  try {
    const raw = window.localStorage.getItem(taskStorageKey(route));
    if (!raw) return null;
    const parsed = JSON.parse(raw) as TaskStore;
    if (typeof parsed !== "object" || parsed === null) return null;
    return { done: parsed.done ?? {}, total: parsed.total ?? 0 };
  } catch {
    return null;
  }
}

const PROGRESS_EVENT = "quickspin:progress";

export function onTaskProgress(listener: () => void): () => void {
  window.addEventListener(PROGRESS_EVENT, listener);
  window.addEventListener("storage", listener);
  return () => {
    window.removeEventListener(PROGRESS_EVENT, listener);
    window.removeEventListener("storage", listener);
  };
}

type TaskProps = PropsWithChildren<{ title: string; id?: string }>;

// Task renders nothing on its own; TaskList reads its props and owns the markup.
export function Task(_props: TaskProps): null {
  return null;
}

export function TaskList({ children }: PropsWithChildren) {
  const route = useContext(DocumentRouteContext);
  const tasks = Children.toArray(children).filter(isValidElement) as ReactElement<TaskProps>[];
  const ids = tasks.map(
    (task, index) => task.props.id ?? (slugify(task.props.title) || String(index)),
  );

  const [done, setDone] = useState<Record<string, boolean>>(
    () => readTaskStore(route)?.done ?? {},
  );

  useEffect(() => {
    window.localStorage.setItem(
      taskStorageKey(route),
      JSON.stringify({ done, total: ids.length } satisfies TaskStore),
    );
    window.dispatchEvent(new Event(PROGRESS_EVENT));
  }, [route, done, ids.length]);

  const doneCount = ids.filter((id) => done[id]).length;
  const percent = ids.length ? Math.round((doneCount / ids.length) * 100) : 0;

  return (
    <section className="task-list">
      <header className="task-list__header">
        <span className="task-list__eyebrow">Implementation tasks</span>
        <span className="task-list__count">
          {doneCount} / {ids.length}
        </span>
      </header>
      <div className="task-list__bar" role="presentation">
        <div style={{ width: `${percent}%` }} />
      </div>
      <ol className="task-list__items">
        {tasks.map((task, index) => {
          const id = ids[index];
          const checked = Boolean(done[id]);
          return (
            <li key={id} className={checked ? "task--done" : ""}>
              <label>
                <input
                  type="checkbox"
                  checked={checked}
                  onChange={() =>
                    setDone((previous) => ({ ...previous, [id]: !previous[id] }))
                  }
                />
                <span className="task__box" aria-hidden="true" />
                <span className="task__body">
                  <strong>{task.props.title}</strong>
                  {task.props.children ? <span>{task.props.children}</span> : null}
                </span>
              </label>
            </li>
          );
        })}
      </ol>
    </section>
  );
}

type HintProps = PropsWithChildren<{ title: string; check?: ReactNode }>;

// Hint renders nothing on its own; HintSteps reads its props and owns the markup.
export function Hint(_props: HintProps): null {
  return null;
}

export function HintSteps({ children }: PropsWithChildren) {
  const hints = Children.toArray(children).filter(isValidElement) as ReactElement<HintProps>[];
  const [revealed, setRevealed] = useState(0);

  return (
    <section className="hint-steps">
      <header className="hint-steps__header">
        <span className="hint-steps__eyebrow">Progressive hints</span>
        <span className="hint-steps__count">
          {revealed} of {hints.length} revealed
        </span>
      </header>
      <p className="hint-steps__note">
        Consult these only when blocked. Hints unlock in order so a later hint cannot
        spoil an earlier task.
      </p>
      <ol className="hint-steps__items">
        {hints.map((hint, index) => {
          const open = index < revealed;
          return (
            <li key={index} className={open ? "hint--open" : "hint--locked"}>
              <div className="hint__title">
                <span className="hint__number">{index + 1}</span>
                <span>{hint.props.title}</span>
              </div>
              {open ? (
                <div className="hint__body">
                  {hint.props.children}
                  {hint.props.check ? (
                    <p className="hint__check">
                      <strong>Back on track when:</strong> {hint.props.check}
                    </p>
                  ) : null}
                </div>
              ) : null}
            </li>
          );
        })}
      </ol>
      <div className="hint-steps__controls">
        {revealed < hints.length ? (
          <button type="button" onClick={() => setRevealed((count) => count + 1)}>
            Reveal hint {revealed + 1}
          </button>
        ) : null}
        {revealed > 0 ? (
          <button
            type="button"
            className="hint-steps__reset"
            onClick={() => setRevealed(0)}
          >
            Hide all
          </button>
        ) : null}
      </div>
    </section>
  );
}

type ResourceKind = "docs" | "article" | "book" | "video" | "paper";

const resourceKindLabels: Record<ResourceKind, string> = {
  docs: "Docs",
  article: "Article",
  book: "Book",
  video: "Video",
  paper: "Paper",
};

export function Resources({ children }: PropsWithChildren) {
  return <ul className="resource-list">{children}</ul>;
}

export function Resource({
  kind = "article",
  title,
  href,
  by,
  children,
}: PropsWithChildren<{
  kind?: ResourceKind;
  title: string;
  href?: string;
  by?: string;
}>) {
  return (
    <li className={`resource resource--${kind}`}>
      <span className="resource__kind">{resourceKindLabels[kind]}</span>
      <div className="resource__body">
        <span className="resource__title">
          {href ? (
            <a href={href} target="_blank" rel="noreferrer">
              {title}
              <span className="external-mark">↗</span>
            </a>
          ) : (
            title
          )}
          {by ? <span className="resource__by"> — {by}</span> : null}
        </span>
        {children ? <span className="resource__note">{children}</span> : null}
      </div>
    </li>
  );
}

export function Figure({
  caption,
  children,
}: PropsWithChildren<{ caption?: string }>) {
  return (
    <figure className="doc-figure">
      <div className="doc-figure__canvas">{children}</div>
      {caption ? <figcaption>{caption}</figcaption> : null}
    </figure>
  );
}

export const mdxComponents: MDXComponents = {
  h1: (props) => <Heading level={1} {...props} />,
  h2: (props) => <Heading level={2} {...props} />,
  h3: (props) => <Heading level={3} {...props} />,
  h4: (props) => <Heading level={4} {...props} />,
  a: SmartLink,
  table: (props) => (
    <div className="table-scroll">
      <table {...props} />
    </div>
  ),
  pre: (props) => (
    <div className="code-frame">
      <span className="code-frame__label">working notes</span>
      <pre {...props} />
    </div>
  ),
  blockquote: (props) => <blockquote className="document-quote" {...props} />,
  Callout,
  StudyPrompt,
  KeyPoint,
  Steps,
  Step,
  Compare,
  CompareItem,
  TaskList,
  Task,
  HintSteps,
  Hint,
  Figure,
  Resources,
  Resource,
};
