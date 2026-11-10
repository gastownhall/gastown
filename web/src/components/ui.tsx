// Small shared pieces: page header, inline error, empty state.
import type { ReactNode } from "react";
import { errorText } from "../api";

export function PageHeader({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <header className="flex flex-wrap items-center gap-3 border-b border-zinc-200 px-6 py-3 dark:border-zinc-800">
      <h1 className="text-base font-semibold">{title}</h1>
      <div className="ml-auto flex flex-wrap items-center gap-3">{children}</div>
    </header>
  );
}

export function InlineError({ error, what }: { error: unknown; what: string }) {
  return (
    <p role="alert" className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-200">
      Could not load {what}: {errorText(error)}
    </p>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="text-sm text-zinc-500">{children}</p>;
}

export function formatTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return "—";
  return d.toLocaleString();
}
