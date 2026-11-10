import { useEffect, useState } from "react";
import { api, errorText } from "../api";
import { PageHeader } from "../components/ui";
import type { EnrollCode } from "../types";

function countdown(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

function useNow(active: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [active]);
  return now;
}

async function copyText(text: string): Promise<boolean> {
  try {
    if (!navigator.clipboard?.writeText) return false;
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

function CopyButton({ text, label }: { text: string; label: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  return (
    <span className="flex items-center gap-2">
      <button
        type="button"
        aria-label={label}
        onClick={async () => setState((await copyText(text)) ? "copied" : "failed")}
        className="rounded-md border border-zinc-300 px-2.5 py-1 text-xs hover:bg-zinc-100 focus-visible:outline-2 focus-visible:outline-teal-500 dark:border-zinc-700 dark:hover:bg-zinc-800"
      >
        {state === "copied" ? "Copied" : "Copy"}
      </button>
      {state === "failed" && <span role="status" className="text-xs text-amber-700 dark:text-amber-400">Copy failed; select the text and copy it manually.</span>}
    </span>
  );
}

export function Settings() {
  const [code, setCode] = useState<EnrollCode | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const now = useNow(code !== null);

  async function create() {
    setBusy(true);
    setError(null);
    try {
      setCode(await api<EnrollCode>("/api/v1/enroll-codes", { method: "POST" }));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  const left = code ? new Date(code.expires_at).getTime() - now : 0;
  const expired = code !== null && left <= 0;
  const command = code ? `gtb join ${window.location.origin} --code ${code.code}` : "";

  return (
    <div>
      <PageHeader title="Settings" />
      <div className="flex max-w-2xl flex-col gap-4 p-6">
        <section aria-labelledby="enroll-h" className="rounded-md border border-zinc-200 p-4 dark:border-zinc-800">
          <h2 id="enroll-h" className="mb-1 text-sm font-semibold">Enroll a machine</h2>
          <p className="mb-3 text-sm text-zinc-500">An enrollment code is single-use and expires after 15 minutes.</p>
          <button
            type="button"
            onClick={create}
            disabled={busy}
            className="rounded-md bg-teal-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-teal-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-teal-500 disabled:opacity-50"
          >
            New enrollment code
          </button>
          {error && (
            <p role="alert" className="mt-3 text-sm text-red-700 dark:text-red-400">
              Could not create a code: {error}
            </p>
          )}
          {code && (
            <div className="mt-4 flex flex-col gap-3">
              <div className="flex items-center gap-3">
                <code className="rounded-md border border-zinc-300 bg-zinc-50 px-4 py-3 font-mono text-2xl tracking-widest select-all dark:border-zinc-700 dark:bg-zinc-900">{code.code}</code>
                <CopyButton text={code.code} label="Copy code" />
              </div>
              <p className={`text-sm ${expired ? "text-red-700 dark:text-red-400" : "text-zinc-500"}`} aria-live="polite">
                {expired ? "Expired; create a new code." : `expires in ${countdown(left)}`}
              </p>
              <div className="flex flex-wrap items-center gap-3">
                <code className="rounded-md bg-zinc-950 px-3 py-2 font-mono text-sm text-zinc-100 select-all">{command}</code>
                <CopyButton text={command} label="Copy join command" />
              </div>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}
