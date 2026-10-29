import { useMemo } from "react";
import { Link } from "react-router";
import { useApi } from "../api";
import { machineNames, useMachines } from "../App";
import { itemHref } from "../components/ItemCard";
import { Empty, InlineError, PageHeader } from "../components/ui";
import type { Board, ItemRow } from "../types";

interface Plan {
  epic: ItemRow;
  tasks: ItemRow[];
}

// plansByMachine groups epics by machine and rig, each with the items whose
// parent is the epic.
export function plansByMachine(items: ItemRow[]): Map<string, Map<string, Plan[]>> {
  const children = new Map<string, ItemRow[]>();
  for (const it of items) {
    if (!it.parent) continue;
    const k = `${it.machine_id}/${it.parent}`;
    if (!children.has(k)) children.set(k, []);
    children.get(k)!.push(it);
  }
  const out = new Map<string, Map<string, Plan[]>>();
  for (const it of items) {
    if (it.kind !== "epic") continue;
    if (!out.has(it.machine_id)) out.set(it.machine_id, new Map());
    const rigs = out.get(it.machine_id)!;
    if (!rigs.has(it.rig)) rigs.set(it.rig, []);
    const tasks = (children.get(`${it.machine_id}/${it.id}`) ?? []).sort((a, b) => a.id.localeCompare(b.id));
    rigs.get(it.rig)!.push({ epic: it, tasks });
  }
  return out;
}

function StepPill({ step }: { step: string }) {
  const done = step === "done";
  return (
    <span className={`rounded-full px-2 py-0.5 font-mono text-[11px] ${done ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300" : "bg-teal-50 text-teal-700 dark:bg-teal-950 dark:text-teal-300"}`}>
      {step}
    </span>
  );
}

export function Coordination() {
  const board = useApi<Board>("/api/v1/board");
  const machines = useMachines();
  const names = useMemo(() => machineNames(machines.data), [machines.data]);
  const plans = useMemo(() => {
    if (!board.data) return null;
    const seen = new Map<string, ItemRow>();
    for (const lane of board.data.lanes) for (const it of lane.items) seen.set(`${it.machine_id}/${it.id}`, it);
    return plansByMachine([...seen.values()]);
  }, [board.data]);

  return (
    <div>
      <PageHeader title="Coordination" />
      <div className="flex flex-col gap-6 p-6">
        <p className="text-xs text-zinc-500">
          Read-only. Plans are approximated from each machine's epics and their child items until the server exposes Mayor plans directly.
        </p>
        {board.error !== undefined && !board.data && <InlineError error={board.error} what="plans" />}
        {plans && plans.size === 0 && <Empty>No epics on any machine.</Empty>}
        {plans &&
          [...plans.entries()].map(([mid, rigs]) => (
            <section key={mid} aria-labelledby={`co-${mid}`}>
              <h2 id={`co-${mid}`} className="mb-3 text-sm font-semibold">
                {names[mid] ?? mid}
              </h2>
              <div className="flex flex-col gap-4">
                {[...rigs.entries()].map(([rig, ps]) => (
                  <div key={rig}>
                    <h3 className="mb-2 font-mono text-xs uppercase tracking-wide text-zinc-500">{rig}</h3>
                    <ul className="flex flex-col gap-3">
                      {ps.map(({ epic, tasks }) => (
                        <li key={epic.id} className="rounded-md border border-zinc-200 p-3 dark:border-zinc-800">
                          <div className="flex items-center gap-2">
                            <Link to={itemHref(epic)} className="font-mono text-xs font-medium text-teal-700 hover:underline focus-visible:outline-2 focus-visible:outline-teal-500 dark:text-teal-400">
                              {epic.id}
                            </Link>
                            <span className="text-sm">{epic.title}</span>
                            <span className="ml-auto">
                              <StepPill step={epic.step} />
                            </span>
                          </div>
                          {tasks.length === 0 ? (
                            <p className="mt-2 text-xs text-zinc-500">No child tasks yet.</p>
                          ) : (
                            <ul className="mt-2 flex flex-col gap-1 border-l border-zinc-200 pl-3 dark:border-zinc-800">
                              {tasks.map((t) => (
                                <li key={t.id} className="flex items-center gap-2 text-sm">
                                  <Link to={itemHref(t)} className="font-mono text-xs text-teal-700 hover:underline focus-visible:outline-2 focus-visible:outline-teal-500 dark:text-teal-400">
                                    {t.id}
                                  </Link>
                                  <span className="truncate">{t.title}</span>
                                  <span className="ml-auto">
                                    <StepPill step={t.step} />
                                  </span>
                                </li>
                              ))}
                            </ul>
                          )}
                        </li>
                      ))}
                    </ul>
                  </div>
                ))}
              </div>
            </section>
          ))}
      </div>
    </div>
  );
}
