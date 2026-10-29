// Adapted from fabro-sh/fabro (MIT): the board card layout of
// apps/fabro-web/app/routes/runs.tsx (PrCard).
import { Link } from "react-router";
import type { ItemRow } from "../types";
import { Freshness } from "./Freshness";

export function MachineBadge({ name }: { name: string }) {
  return (
    <span className="truncate rounded border border-zinc-300 px-1.5 py-0.5 font-mono text-[11px] text-zinc-600 dark:border-zinc-700 dark:text-zinc-400">
      {name}
    </span>
  );
}

export function itemHref(it: { machine_id: string; id: string }) {
  return `/items/${encodeURIComponent(it.machine_id)}/${encodeURIComponent(it.id)}`;
}

export function ItemCard({ item, machineName, fetchedAt, showWaiting }: { item: ItemRow; machineName: string; fetchedAt: number | undefined; showWaiting?: boolean }) {
  return (
    <Link
      to={itemHref(item)}
      className="group block rounded-md border border-zinc-200 bg-white p-3 transition-all duration-200 hover:border-zinc-400 hover:shadow-md focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-teal-500 dark:border-zinc-800 dark:bg-zinc-900 dark:hover:border-zinc-600 dark:hover:shadow-black/30"
    >
      <div className="mb-1.5 flex items-center gap-1.5">
        <span className="font-mono text-xs font-medium text-teal-600 dark:text-teal-400">{item.id}</span>
        <span className="rounded-full border border-zinc-200 px-1.5 py-0.5 font-mono text-[11px] uppercase tracking-wide text-zinc-500 dark:border-zinc-700">
          {item.kind}
        </span>
        <span className="ml-auto">
          <Freshness seconds={item.freshness_seconds} fetchedAt={fetchedAt} />
        </span>
      </div>
      <p className="text-sm leading-snug text-zinc-800 dark:text-zinc-200">{item.title}</p>
      {showWaiting && item.waiting_for_human && (
        <p className="mt-2 text-xs italic text-amber-700 dark:text-amber-400">{item.waiting_for_human}</p>
      )}
      <div className="mt-2 flex items-center gap-1.5 text-xs text-zinc-500">
        <span className="font-mono">{item.rig}</span>
        <MachineBadge name={machineName} />
        {showWaiting && <span className="ml-auto font-mono text-[11px]">{item.step}</span>}
      </div>
    </Link>
  );
}
