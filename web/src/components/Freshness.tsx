// Freshness is how long ago the machine last reported. The server's
// freshness_seconds is as of the fetch; the badge ages it on the client so a
// machine that goes quiet turns grey without a refetch.
import { useClock } from "../clock";

// staleAfterSeconds mirrors the server's staleAfter (internal/server/webapi.go).
export const staleAfterSeconds = 120;

export function freshnessLabel(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  if (s < 60) return `${s}s ago`;
  return `${Math.floor(s / 60)}m ago`;
}

// ageSeconds is freshness_seconds plus the time since the response arrived.
export function ageSeconds(seconds: number, fetchedAt: number | undefined, now: number): number {
  const since = fetchedAt === undefined ? 0 : Math.max(0, (now - fetchedAt) / 1000);
  return seconds + since;
}

export function Freshness({ seconds, fetchedAt }: { seconds: number; fetchedAt: number | undefined }) {
  const now = useClock();
  const age = ageSeconds(seconds, fetchedAt, now);
  if (age > staleAfterSeconds) {
    return (
      <span
        className="rounded-full bg-zinc-200 px-1.5 py-0.5 font-mono text-[11px] text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400"
        title={`machine last seen ${freshnessLabel(age)}`}
      >
        stale
      </span>
    );
  }
  return (
    <span className="rounded-full bg-emerald-50 px-1.5 py-0.5 font-mono text-[11px] text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300" title="machine last seen">
      {freshnessLabel(age)}
    </span>
  );
}
