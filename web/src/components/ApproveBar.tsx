// ApproveBar approves, or requests changes on, exactly the artifact version
// the user is looking at (the pinned sha, never a newer refetched one), then
// polls the request until the machine
// answers, it expires, or 60 s pass.
import { useEffect, useRef, useState } from "react";
import { api, errorText, itemPath, pollRequest, revalidate, type Outcome } from "../api";
import type { Accepted, ArtifactView } from "../types";

type State =
  | { kind: "idle" }
  | { kind: "pending" }
  | { kind: "message"; text: string; tone: "ok" | "warn" | "error" };

export function outcomeText(o: Outcome, action: "approve" | "request-changes"): { text: string; tone: "ok" | "warn" | "error" } {
  switch (o.kind) {
    case "done":
      if (o.ok) return { text: action === "approve" ? "Approved" : "Changes requested", tone: "ok" };
      if (o.error === "expired") return { text: "Request expired; the machine was offline", tone: "warn" };
      if (o.error === "stale_sha") return { text: "Artifact changed on the machine; reload to review the new version", tone: "warn" };
      return { text: `Machine reported: ${o.error}`, tone: "error" };
    case "expired":
      return { text: "Request expired; the machine was offline", tone: "warn" };
    case "timeout":
      return { text: "No answer from the machine yet; the request stays queued", tone: "warn" };
  }
}

const toneClass = {
  ok: "text-emerald-700 dark:text-emerald-400",
  warn: "text-amber-700 dark:text-amber-400",
  error: "text-red-700 dark:text-red-400",
};

const btn =
  "rounded-md px-3 py-1.5 text-sm font-medium focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-teal-500 disabled:cursor-not-allowed disabled:opacity-50";

export function ApproveBar({ machine, itemId, artifact, disabled = false }: { machine: string; itemId: string; artifact: ArtifactView; disabled?: boolean }) {
  const [state, setState] = useState<State>({ kind: "idle" });
  const ctl = useRef<AbortController | null>(null);
  useEffect(() => () => ctl.current?.abort(), []);
  // The parent keys this bar by artifact name and pinned sha, so a newly
  // loaded version starts as a fresh review.

  async function act(action: "approve" | "request-changes") {
    ctl.current?.abort();
    const c = new AbortController();
    ctl.current = c;
    setState({ kind: "pending" });
    try {
      const acc = await api<Accepted>(`${itemPath(machine, itemId)}/${action}`, {
        method: "POST",
        body: { artifact: artifact.name, sha: artifact.sha },
        signal: c.signal,
      });
      const outcome = await pollRequest(machine, acc.key, c.signal);
      if (c.signal.aborted) return;
      setState({ kind: "message", ...outcomeText(outcome, action) });
      revalidate();
    } catch (e) {
      if (c.signal.aborted) return;
      setState({ kind: "message", text: `Could not send: ${errorText(e)}`, tone: "error" });
    }
  }

  const pending = state.kind === "pending";
  const off = pending || disabled;
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-md border border-zinc-200 bg-zinc-50 px-3 py-2 dark:border-zinc-800 dark:bg-zinc-900">
      <span className="text-xs text-zinc-500">
        {artifact.name} @ <code className="font-mono text-zinc-700 dark:text-zinc-300" title={artifact.sha}>{artifact.sha.slice(0, 12)}</code>
      </span>
      <div className="ml-auto flex items-center gap-2">
        <span aria-live="polite" className="text-sm">
          {pending && <span className="text-zinc-500">Waiting for machine…</span>}
          {state.kind === "message" && <span className={toneClass[state.tone]}>{state.text}</span>}
        </span>
        <button type="button" className={`${btn} border border-zinc-300 bg-white text-zinc-800 hover:bg-zinc-100 dark:border-zinc-700 dark:bg-zinc-800 dark:text-zinc-100 dark:hover:bg-zinc-700`} disabled={off} onClick={() => act("request-changes")}>
          Request changes
        </button>
        <button type="button" className={`${btn} bg-teal-600 text-white hover:bg-teal-700`} disabled={off} onClick={() => act("approve")}>
          Approve
        </button>
      </div>
    </div>
  );
}
