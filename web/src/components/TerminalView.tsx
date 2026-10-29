// TerminalView shows a peek of the item's worker terminal. It refreshes on
// demand and every timing.terminalMs while the page is visible. Each effect
// run (one machine/item) owns an AbortController: unmounting or switching
// items aborts the in-flight peek, and a response from an earlier run never
// writes state or reschedules. Peeks within one run never overlap (the
// server may wait for the machine).
import { useEffect, useRef, useState } from "react";
import { api, errorText, itemPath, timing } from "../api";

interface View {
  lines: string | null;
  error: string | null;
  busy: boolean;
}

export function TerminalView({ machine, itemId }: { machine: string; itemId: string }) {
  const [view, setView] = useState<View>({ lines: null, error: null, busy: false });
  const refreshRef = useRef<() => void>(() => {});

  useEffect(() => {
    const ctl = new AbortController();
    const { signal } = ctl;
    let inflight = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    setView({ lines: null, error: null, busy: false });

    const schedule = () => {
      clearTimeout(timer);
      if (!signal.aborted && document.visibilityState === "visible") {
        timer = setTimeout(() => void refresh(), timing.terminalMs);
      }
    };

    async function refresh() {
      if (inflight || signal.aborted) return;
      inflight = true;
      clearTimeout(timer);
      setView((v) => ({ ...v, busy: true }));
      try {
        const res = await api<{ lines: string }>(`${itemPath(machine, itemId)}/terminal`, { signal });
        if (signal.aborted) return;
        setView({ lines: res.lines, error: null, busy: false });
      } catch (e) {
        if (signal.aborted) return;
        setView((v) => ({ ...v, error: errorText(e), busy: false }));
      } finally {
        inflight = false;
      }
      schedule();
    }

    const onVis = () => {
      if (document.visibilityState === "visible") void refresh();
      else clearTimeout(timer);
    };
    refreshRef.current = () => void refresh();
    document.addEventListener("visibilitychange", onVis);
    void refresh();
    return () => {
      ctl.abort();
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVis);
      refreshRef.current = () => {};
    };
  }, [machine, itemId]);

  const { lines, error, busy } = view;
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-3">
        <button
          type="button"
          aria-label="Refresh terminal"
          onClick={() => refreshRef.current()}
          disabled={busy}
          className="rounded-md border border-zinc-300 px-3 py-1 text-sm hover:bg-zinc-100 focus-visible:outline-2 focus-visible:outline-teal-500 disabled:opacity-50 dark:border-zinc-700 dark:hover:bg-zinc-800"
        >
          {busy ? "Refreshing…" : "Refresh"}
        </button>
        <span className="text-xs text-zinc-500">Auto-refreshes every {Math.round(timing.terminalMs / 1000)} s while visible.</span>
      </div>
      {error && (
        <p role="alert" className="text-sm text-red-700 dark:text-red-400">
          Terminal unavailable: {error}
        </p>
      )}
      <pre className="max-h-[70vh] min-h-40 overflow-auto rounded-md bg-zinc-950 p-3 font-mono text-xs leading-relaxed text-zinc-100" aria-label="Terminal output">
        {lines ?? (error ? "" : "Loading…")}
      </pre>
    </div>
  );
}
