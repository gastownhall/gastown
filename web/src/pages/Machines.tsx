import { useEffect, useRef, useState } from "react";
import { api, BACKSTOP_MS, errorText, revalidate } from "../api";
import { useMachines } from "../App";
import { Freshness } from "../components/Freshness";
import { useToast } from "../components/Toast";
import { Empty, InlineError, PageHeader, formatTime } from "../components/ui";
import type { Machine } from "../types";

const CONFIRM_MS = 5000;

const btn = "rounded-md px-2.5 py-1 text-xs font-medium focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-teal-500 disabled:opacity-50";

// RevokeButton needs two clicks: Revoke, then Confirm revoke within 5 s.
function RevokeButton({ m }: { m: Machine }) {
  const [armed, setArmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const toast = useToast();
  useEffect(() => () => clearTimeout(timer.current), []);

  if (m.revoked_at) return <span className="text-xs text-zinc-500">Revoked {formatTime(m.revoked_at)}</span>;

  async function confirm() {
    clearTimeout(timer.current);
    setBusy(true);
    try {
      await api(`/api/v1/machines/${encodeURIComponent(m.id)}`, { method: "DELETE" });
      toast(`Revoked ${m.hostname || m.id}`, "info");
      revalidate();
    } catch (e) {
      toast(`Revoke failed: ${errorText(e)}`);
    } finally {
      setBusy(false);
      setArmed(false);
    }
  }

  if (armed) {
    return (
      <span className="flex items-center gap-2">
        <button type="button" disabled={busy} className={`${btn} bg-red-600 text-white hover:bg-red-700`} onClick={confirm} aria-label={`Confirm revoke ${m.hostname || m.id}`}>
          Confirm revoke
        </button>
        <button type="button" className={`${btn} text-zinc-600 hover:bg-zinc-100 dark:text-zinc-400 dark:hover:bg-zinc-800`} onClick={() => setArmed(false)}>
          Cancel
        </button>
      </span>
    );
  }
  return (
    <button
      type="button"
      className={`${btn} border border-red-300 text-red-700 hover:bg-red-50 dark:border-red-800 dark:text-red-400 dark:hover:bg-red-950`}
      aria-label={`Revoke ${m.hostname || m.id}`}
      onClick={() => {
        setArmed(true);
        timer.current = setTimeout(() => setArmed(false), CONFIRM_MS);
      }}
    >
      Revoke
    </button>
  );
}

export function Machines() {
  const machines = useMachines({ refreshInterval: BACKSTOP_MS });
  const list = machines.data;
  return (
    <div>
      <PageHeader title="Machines" />
      <div className="p-6">
        {machines.error !== undefined && !list && <InlineError error={machines.error} what="machines" />}
        {list && list.length === 0 && <Empty>No machines enrolled. Create an enrollment code in Settings.</Empty>}
        {list && list.length > 0 && (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs uppercase tracking-wide text-zinc-500">
                  <th className="py-2 pr-4 font-medium">Hostname</th>
                  <th className="py-2 pr-4 font-medium">OS / arch</th>
                  <th className="py-2 pr-4 font-medium">gtb version</th>
                  <th className="py-2 pr-4 font-medium">Last seen</th>
                  <th className="py-2 pr-4 font-medium">Rigs</th>
                  <th className="py-2 font-medium"><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-200 dark:divide-zinc-800">
                {list.map((m) => (
                  <tr key={m.id} className={m.revoked_at ? "opacity-60" : ""}>
                    <td className="py-2 pr-4">
                      <div className="font-medium">{m.hostname || "—"}</div>
                      <div className="font-mono text-xs text-zinc-500">{m.id}</div>
                    </td>
                    <td className="py-2 pr-4 font-mono">{m.os}/{m.arch}</td>
                    <td className="py-2 pr-4 font-mono">{m.gtb_version || "—"}</td>
                    <td className="py-2 pr-4">
                      <span className="flex items-center gap-2">
                        <Freshness seconds={m.freshness_seconds} fetchedAt={machines.fetchedAt} />
                        <span className="text-xs text-zinc-500">{formatTime(m.last_seen_at)}</span>
                      </span>
                    </td>
                    <td className="py-2 pr-4 font-mono text-xs">{(m.rigs ?? []).map((r) => r.name).join(", ") || "—"}</td>
                    <td className="py-2 text-right">
                      <RevokeButton m={m} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  );
}
