import { useEffect, useMemo, useState, type KeyboardEvent } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { BACKSTOP_MS, itemPath, useApi } from "../api";
import { machineNames, useMachines } from "../App";
import { ApproveBar } from "../components/ApproveBar";
import { Freshness } from "../components/Freshness";
import { Markdown } from "../components/Markdown";
import { TerminalView } from "../components/TerminalView";
import { Empty, InlineError, formatTime } from "../components/ui";
import type { ArtifactView, ItemDetail, ItemEvent } from "../types";

const TABS = [
  { id: "overview", label: "Overview" },
  { id: "artifacts", label: "Artifacts" },
  { id: "evidence", label: "Evidence" },
  { id: "logs", label: "Logs" },
  { id: "files", label: "Files" },
  { id: "usage", label: "Usage" },
  { id: "terminal", label: "Terminal" },
] as const;
type TabId = (typeof TABS)[number]["id"];

function obj(v: unknown): Record<string, unknown> {
  return v && typeof v === "object" ? (v as Record<string, unknown>) : {};
}

function str(v: unknown): string {
  return typeof v === "string" ? v : v === undefined || v === null ? "" : String(v);
}

// stepHistory derives step entries from item.upsert events (oldest first),
// keeping only events where the step changed.
export function stepHistory(events: ItemEvent[]): { step: string; at: string }[] {
  const out: { step: string; at: string }[] = [];
  for (const ev of [...events].reverse()) {
    if (ev.type !== "item.upsert") continue;
    const d = obj(ev.data);
    const step = str(d.step);
    if (!step || out.at(-1)?.step === step) continue;
    out.push({ step, at: str(d.step_entered_at) || ev.at });
  }
  return out;
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-[11px] uppercase tracking-wide text-zinc-500">{label}</dt>
      <dd className={`truncate text-sm ${mono ? "font-mono" : ""}`}>{value || "—"}</dd>
    </div>
  );
}

function Overview({ d }: { d: ItemDetail }) {
  const history = stepHistory(d.events);
  const it = d.item;
  return (
    <div className="grid gap-6 md:grid-cols-2">
      <section aria-labelledby="ov-gate">
        <h3 id="ov-gate" className="mb-2 text-sm font-semibold">Current gate</h3>
        <p className="text-sm">
          At step <span className="font-mono">{it.step}</span> since {formatTime(it.step_entered_at)}.
        </p>
        {it.waiting_for_human ? (
          <p className="mt-2 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200">
            Waiting on you: {it.waiting_for_human}
          </p>
        ) : (
          <p className="mt-2 text-sm text-zinc-500">Not waiting on a human.</p>
        )}
        {it.depends_on && it.depends_on.length > 0 && (
          <p className="mt-2 text-sm">
            Depends on <span className="font-mono">{it.depends_on.join(", ")}</span>
          </p>
        )}
      </section>
      <section aria-labelledby="ov-history">
        <h3 id="ov-history" className="mb-2 text-sm font-semibold">Step history</h3>
        {history.length === 0 ? (
          <Empty>No transitions recorded yet.</Empty>
        ) : (
          <ol className="flex flex-col gap-1">
            {history.map((h, i) => (
              <li key={`${h.step}-${i}`} className="flex items-center gap-2 text-sm">
                <span className="rounded-full bg-teal-50 px-2 py-0.5 font-mono text-xs text-teal-700 dark:bg-teal-950 dark:text-teal-300">{h.step}</span>
                <span className="text-xs text-zinc-500">{formatTime(h.at)}</span>
              </li>
            ))}
          </ol>
        )}
      </section>
    </div>
  );
}

// Artifacts pins the version of each artifact the user opened (name, sha,
// content). A live refetch that brings a newer sha does not replace what is
// on screen or what Approve posts: a banner offers to load it, and approving
// the pinned sha gets stale_sha from the machine (R43). If the pinned
// artifact disappears from the latest data, a banner says so and approval is
// disabled (R45). The component is keyed by item, so pins never cross items.
function Artifacts({ d, machine }: { d: ItemDetail; machine: string }) {
  const [params, setParams] = useSearchParams();
  const [pins, setPins] = useState<Record<string, ArtifactView>>({});
  // The artifact first shown without ?artifact= stays selected even if the
  // list changes under it.
  const [defaultName, setDefaultName] = useState<string | undefined>(d.artifacts[0]?.name);
  const first = d.artifacts[0]?.name;
  useEffect(() => {
    if (defaultName === undefined && first !== undefined) setDefaultName(first);
  }, [defaultName, first]);
  const selName = params.get("artifact") ?? defaultName ?? "";
  const latest = d.artifacts.find((a) => a.name === selName);
  const pinned = pins[selName] ?? latest;

  useEffect(() => {
    if (latest && !pins[latest.name]) setPins((p) => (p[latest.name] ? p : { ...p, [latest.name]: latest }));
  }, [latest, pins]);

  if (!pinned) return <Empty>{d.artifacts.length === 0 ? "No artifacts yet." : `No artifact named ${selName}.`}</Empty>;
  const gone = latest === undefined;
  const newer = latest && latest.sha !== pinned.sha ? latest : null;

  function open(a: ArtifactView) {
    // Picking an artifact (again) re-pins it to the latest version.
    setPins((p) => ({ ...p, [a.name]: a }));
    setParams(
      (p) => {
        const n = new URLSearchParams(p);
        n.set("artifact", a.name);
        return n;
      },
      { replace: true },
    );
  }

  return (
    <div className="flex flex-col gap-4 md:flex-row">
      <ul aria-label="Artifacts" className="flex shrink-0 flex-row gap-1 md:w-40 md:flex-col">
        {d.artifacts.map((a) => (
          <li key={a.name}>
            <button
              type="button"
              aria-pressed={a.name === pinned.name}
              onClick={() => open(a)}
              className={`w-full rounded-md px-3 py-1.5 text-left font-mono text-sm focus-visible:outline-2 focus-visible:outline-teal-500 ${
                a.name === pinned.name ? "bg-zinc-200 dark:bg-zinc-800" : "hover:bg-zinc-100 dark:hover:bg-zinc-900"
              }`}
            >
              {a.name}
            </button>
          </li>
        ))}
      </ul>
      <div className="flex min-w-0 flex-1 flex-col gap-3">
        {gone && (
          <div role="status" className="rounded-md border border-red-300 bg-red-50 px-3 py-2 text-sm text-red-800 dark:border-red-800 dark:bg-red-950 dark:text-red-200">
            {pinned.name} is no longer on the machine.
          </div>
        )}
        {newer && (
          <div role="status" className="flex flex-wrap items-center gap-3 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200">
            <span>A newer version of {pinned.name} is on the machine.</span>
            <button
              type="button"
              onClick={() => open(newer)}
              className="rounded-md border border-amber-400 px-2.5 py-1 text-xs font-medium hover:bg-amber-100 focus-visible:outline-2 focus-visible:outline-teal-500 dark:border-amber-700 dark:hover:bg-amber-900"
            >
              Load it
            </button>
          </div>
        )}
        <ApproveBar key={`${pinned.name}@${pinned.sha}`} machine={machine} itemId={d.item.id} artifact={pinned} disabled={gone} />
        <article className="rounded-md border border-zinc-200 p-4 dark:border-zinc-800">
          <Markdown source={pinned.content} />
        </article>
      </div>
    </div>
  );
}

function Evidence({ d }: { d: ItemDetail }) {
  const runs = d.events.filter((e) => e.type === "evidence").map((e) => ({ ev: e, d: obj(e.data) })).filter((r) => r.d.kind === "tdd" || r.d.kind === "check" || r.d.kind === "approval");
  if (runs.length === 0) return <Empty>No evidence recorded yet.</Empty>;
  return (
    <ul className="flex flex-col gap-3">
      {runs.map(({ ev, d: x }) => {
        const kind = str(x.kind);
        const passed = kind === "tdd" ? Number(x.exit) === 0 : kind === "check" ? x.passed === true : x.verdict === "approved";
        const title =
          kind === "tdd"
            ? `TDD ${str(x.phase)}${x.plan_step !== undefined && x.plan_step !== "" ? ` · step ${str(x.plan_step)}` : ""} · exit ${str(x.exit)}`
            : kind === "check"
              ? `Check run · ${x.passed ? "passed" : "failed"}`
              : `Approval · ${str(x.artifact)} ${str(x.verdict)} by ${str(x.actor) || str(x.role)}`;
        return (
          <li key={ev.id} className="rounded-md border border-zinc-200 p-3 dark:border-zinc-800">
            <div className="mb-1 flex items-center gap-2 text-sm">
              <span aria-hidden="true" className={`h-2 w-2 rounded-full ${passed ? "bg-emerald-500" : "bg-red-500"}`} />
              <span className="font-medium">{title}</span>
              <span className="ml-auto text-xs text-zinc-500">{formatTime(ev.at)}</span>
            </div>
            {str(x.cmd) && <pre className="overflow-x-auto font-mono text-xs text-zinc-600 dark:text-zinc-400">{str(x.cmd)}</pre>}
            {str(x.tail) && <pre className="mt-2 max-h-80 overflow-auto rounded bg-zinc-950 p-2 font-mono text-xs text-zinc-100">{str(x.tail)}</pre>}
          </li>
        );
      })}
    </ul>
  );
}

function Logs({ d }: { d: ItemDetail }) {
  if (d.events.length === 0) return <Empty>No events.</Empty>;
  return (
    <ol className="flex flex-col divide-y divide-zinc-200 dark:divide-zinc-800">
      {d.events.map((e) => (
        <li key={e.id} className="py-2">
          <div className="flex items-center gap-2 text-xs">
            <span className="font-mono text-zinc-500">{formatTime(e.at)}</span>
            <span className="rounded bg-zinc-100 px-1.5 py-0.5 font-mono dark:bg-zinc-800">{e.type}</span>
          </div>
          {e.data !== null && e.data !== undefined && (
            <pre className="mt-1 overflow-x-auto font-mono text-xs text-zinc-600 dark:text-zinc-400">{JSON.stringify(e.data, null, 1)}</pre>
          )}
        </li>
      ))}
    </ol>
  );
}

function Files({ d }: { d: ItemDetail }) {
  if (d.diffstat.length === 0) return <Empty>No file changes reported.</Empty>;
  const add = d.diffstat.reduce((s, f) => s + f.added, 0);
  const del = d.diffstat.reduce((s, f) => s + f.deleted, 0);
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-xs uppercase tracking-wide text-zinc-500">
            <th className="py-1 pr-4 font-medium">File</th>
            <th className="py-1 pr-4 text-right font-medium">Added</th>
            <th className="py-1 text-right font-medium">Deleted</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-200 dark:divide-zinc-800">
          {d.diffstat.map((f) => (
            <tr key={f.path}>
              <td className="py-1 pr-4 font-mono">{f.path}</td>
              <td className="py-1 pr-4 text-right font-mono text-emerald-700 dark:text-emerald-400">+{f.added}</td>
              <td className="py-1 text-right font-mono text-red-700 dark:text-red-400">−{f.deleted}</td>
            </tr>
          ))}
        </tbody>
        <tfoot>
          <tr className="border-t border-zinc-300 text-xs text-zinc-500 dark:border-zinc-700">
            <td className="py-1 pr-4">{d.diffstat.length} files</td>
            <td className="py-1 pr-4 text-right font-mono">+{add}</td>
            <td className="py-1 text-right font-mono">−{del}</td>
          </tr>
        </tfoot>
      </table>
    </div>
  );
}

function UsageTab({ d }: { d: ItemDetail }) {
  const u = d.item.usage;
  if (!u) return <Empty>No usage reported</Empty>;
  return (
    <dl className="grid max-w-md grid-cols-3 gap-4">
      <Field label="Input tokens" value={u.input_tokens.toLocaleString()} mono />
      <Field label="Output tokens" value={u.output_tokens.toLocaleString()} mono />
      <Field label="Cost" value={`$${u.cost_usd.toFixed(2)}`} mono />
    </dl>
  );
}

export function Item() {
  const { machine = "", id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const raw = params.get("tab");
  const tab: TabId = TABS.some((t) => t.id === raw) ? (raw as TabId) : "overview";
  const detail = useApi<ItemDetail>(itemPath(machine, id), { refreshInterval: BACKSTOP_MS });
  const machines = useMachines();
  const names = useMemo(() => machineNames(machines.data), [machines.data]);

  function selectTab(t: TabId) {
    setParams(
      (p) => {
        const n = new URLSearchParams(p);
        n.set("tab", t);
        return n;
      },
      { replace: true },
    );
  }
  function onTabKey(e: KeyboardEvent) {
    const i = TABS.findIndex((t) => t.id === tab);
    let j = -1;
    if (e.key === "ArrowRight") j = (i + 1) % TABS.length;
    else if (e.key === "ArrowLeft") j = (i - 1 + TABS.length) % TABS.length;
    else if (e.key === "Home") j = 0;
    else if (e.key === "End") j = TABS.length - 1;
    if (j < 0) return;
    e.preventDefault();
    selectTab(TABS[j].id);
    document.getElementById(`tab-${TABS[j].id}`)?.focus();
  }

  const d = detail.data;
  return (
    <div className="flex flex-col">
      <header className="border-b border-zinc-200 px-6 py-4 dark:border-zinc-800">
        <nav aria-label="Breadcrumb" className="mb-1 text-xs text-zinc-500">
          <Link to="/" className="hover:underline focus-visible:outline-2 focus-visible:outline-teal-500">Board</Link> / <span className="font-mono">{id}</span>
        </nav>
        <h1 className="text-lg font-semibold">
          <span className="mr-2 font-mono text-teal-700 dark:text-teal-400">{id}</span>
          {d?.item.title}
        </h1>
        {d && (
          <dl className="mt-3 grid grid-cols-2 gap-x-6 gap-y-2 sm:grid-cols-3 lg:grid-cols-6">
            <Field label="Kind" value={d.item.kind} />
            <Field label="Step" value={d.item.step} mono />
            <Field label="Machine" value={names[d.item.machine_id] ?? d.item.machine_id} mono />
            <Field label="Rig" value={d.item.rig} mono />
            <Field label="Branch" value={d.item.branch} mono />
            <div>
              <dt className="text-[11px] uppercase tracking-wide text-zinc-500">Freshness</dt>
              <dd>
                <Freshness seconds={d.item.freshness_seconds} fetchedAt={detail.fetchedAt} />
              </dd>
            </div>
          </dl>
        )}
      </header>
      {detail.error !== undefined && !d && (
        <div className="p-6">
          <InlineError error={detail.error} what="this item" />
        </div>
      )}
      {!d && detail.loading && <p className="p-6 text-sm text-zinc-500">Loading…</p>}
      {d && (
        <>
          <div role="tablist" aria-label="Item sections" className="flex gap-1 overflow-x-auto border-b border-zinc-200 px-6 dark:border-zinc-800" onKeyDown={onTabKey}>
            {TABS.map((t) => (
              <button
                key={t.id}
                id={`tab-${t.id}`}
                type="button"
                role="tab"
                aria-selected={t.id === tab}
                aria-controls={`panel-${t.id}`}
                tabIndex={t.id === tab ? 0 : -1}
                onClick={() => selectTab(t.id)}
                className={`-mb-px border-b-2 px-3 py-2 text-sm focus-visible:outline-2 focus-visible:outline-teal-500 ${
                  t.id === tab ? "border-teal-600 font-medium text-zinc-900 dark:text-zinc-100" : "border-transparent text-zinc-500 hover:text-zinc-800 dark:hover:text-zinc-200"
                }`}
              >
                {t.label}
              </button>
            ))}
          </div>
          <div role="tabpanel" id={`panel-${tab}`} aria-labelledby={`tab-${tab}`} className="p-6">
            {tab === "overview" && <Overview d={d} />}
            {tab === "artifacts" && <Artifacts key={`${machine}/${id}`} d={d} machine={machine} />}
            {tab === "evidence" && <Evidence d={d} />}
            {tab === "logs" && <Logs d={d} />}
            {tab === "files" && <Files d={d} />}
            {tab === "usage" && <UsageTab d={d} />}
            {tab === "terminal" && <TerminalView machine={machine} itemId={id} />}
          </div>
        </>
      )}
    </div>
  );
}

