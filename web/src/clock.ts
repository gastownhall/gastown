// One shared clock for every freshness badge: a single interval, started
// with the first subscriber and stopped with the last.
import { useSyncExternalStore } from "react";

export const TICK_MS = 5000;

let now = Date.now();
let timer: ReturnType<typeof setInterval> | undefined;
const subs = new Set<() => void>();

function subscribe(fn: () => void) {
  subs.add(fn);
  if (timer === undefined) {
    now = Date.now();
    timer = setInterval(() => {
      now = Date.now();
      for (const s of [...subs]) s();
    }, TICK_MS);
  }
  return () => {
    subs.delete(fn);
    if (subs.size === 0 && timer !== undefined) {
      clearInterval(timer);
      timer = undefined;
    }
  };
}

const snapshot = () => now;

// useClock returns the time of the latest shared tick (ms).
export function useClock(): number {
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}
