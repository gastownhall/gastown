// Adapted from fabro-sh/fabro (MIT): the column layout of
// apps/fabro-web/app/routes/runs.tsx (BoardColumnView).
import { useMemo, type ReactNode } from "react";
import { useSearchParams } from "react-router";
import { BACKSTOP_MS, useApi } from "../api";
import { machineNames, useMachines } from "../App";
import { ItemCard } from "../components/ItemCard";
import { InlineError, PageHeader } from "../components/ui";
import type { Board as BoardData, ItemRow } from "../types";

const KINDS = ["task", "fix", "epic"];

const selectClass =
  "rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm focus-visible:outline-2 focus-visible:outline-teal-500 dark:border-zinc-700 dark:bg-zinc-900";

function Filter({ label, value, options, onChange }: { label: string; value: string; options: { value: string; label: string }[]; onChange: (v: string) => void }) {
  const id = `filter-${label.toLowerCase()}`;
  return (
    <span className="flex items-center gap-1.5">
      <label htmlFor={id} className="text-xs text-zinc-500">
        {label}
      </label>
      <select id={id} className={selectClass} value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">All</option>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
        {value && !options.some((o) => o.value === value) && <option value={value}>{value}</option>}
      </select>
    </span>
  );
}

function Column({ title, count, accent, children }: { title: string; count: number; accent: string; children: ReactNode }) {
  return (
    <section aria-label={title} className="flex w-72 shrink-0 flex-col">
      <h2 className="mb-3 flex items-center gap-2 text-sm font-semibold tracking-wide text-zinc-700 dark:text-zinc-300">
        <span aria-hidden="true" className={`h-2.5 w-2.5 rounded-full ${accent}`} />
        <span>{title}</span>
        <span className="rounded-full bg-zinc-100 px-2 py-0.5 font-mono text-xs font-normal text-zinc-500 dark:bg-zinc-800" aria-label={`${count} items`}>{count}</span>
      </h2>
      <div className="flex flex-col gap-2">{children}</div>
    </section>
  );
}

export function Board() {
  const [params, setParams] = useSearchParams();
  const machine = params.get("machine") ?? "";
  const rig = params.get("rig") ?? "";
  const kind = params.get("kind") ?? "";
  const machines = useMachines();
  const names = useMemo(() => machineNames(machines.data), [machines.data]);

  const q = new URLSearchParams();
  if (machine) q.set("machine", machine);
  if (rig) q.set("rig", rig);
  if (kind) q.set("kind", kind);
  const qs = q.toString();
  const board = useApi<BoardData>(`/api/v1/board${qs ? `?${qs}` : ""}`, { refreshInterval: BACKSTOP_MS });

  const rigOptions = useMemo(() => {
    const set = new Set<string>();
    for (const m of machines.data ?? []) {
      if (machine && m.id !== machine) continue;
      for (const r of m.rigs ?? []) set.add(r.name);
    }
    return [...set].sort().map((r) => ({ value: r, label: r }));
  }, [machines.data, machine]);

  function setFilter(key: string, value: string) {
    setParams(
      (p) => {
        const n = new URLSearchParams(p);
        if (value) n.set(key, value);
        else n.delete(key);
        return n;
      },
      { replace: true },
    );
  }

  const name = (it: ItemRow) => names[it.machine_id] ?? it.machine_id;
  const data = board.data;

  return (
    <div className="flex h-full flex-col">
      <PageHeader title="Board">
        <Filter label="Machine" value={machine} options={(machines.data ?? []).map((m) => ({ value: m.id, label: m.hostname || m.id }))} onChange={(v) => setFilter("machine", v)} />
        <Filter label="Rig" value={rig} options={rigOptions} onChange={(v) => setFilter("rig", v)} />
        <Filter label="Kind" value={kind} options={KINDS.map((k) => ({ value: k, label: k }))} onChange={(v) => setFilter("kind", v)} />
      </PageHeader>
      <div className="flex-1 overflow-x-auto p-6">
        {board.error !== undefined && !data && <InlineError error={board.error} what="the board" />}
        {data && (
          <div className="flex gap-5">
            <Column title="Needs you" count={data.needs_you.length} accent="bg-amber-500">
              {data.needs_you.length === 0 ? (
                <p className="rounded-md border border-dashed border-zinc-300 p-3 text-sm text-zinc-500 dark:border-zinc-700">Nothing waiting on you</p>
              ) : (
                data.needs_you.map((it) => <ItemCard key={`${it.machine_id}/${it.id}`} item={it} machineName={name(it)} fetchedAt={board.fetchedAt} showWaiting />)
              )}
            </Column>
            {data.lanes.map((lane) => (
              <Column key={lane.step} title={lane.step} count={lane.items.length} accent={lane.step === "done" ? "bg-emerald-500" : "bg-teal-500"}>
                {lane.items.map((it) => (
                  <ItemCard key={`${it.machine_id}/${it.id}`} item={it} machineName={name(it)} fetchedAt={board.fetchedAt} />
                ))}
              </Column>
            ))}
          </div>
        )}
        {!data && board.loading && <p className="text-sm text-zinc-500">Loading…</p>}
      </div>
    </div>
  );
}
