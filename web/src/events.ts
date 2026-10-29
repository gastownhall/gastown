// One EventSource on /api/v1/events. board.changed revalidates every mounted
// useApi hook. On error the stream is closed, the session is probed (a 401
// redirects to /login) and the stream reopens with exponential backoff.
import { useEffect } from "react";
import { ApiError, api, revalidate } from "./api";

const maxBackoffMs = 30_000;

export function useBoardEvents() {
  useEffect(() => {
    if (typeof EventSource === "undefined") return;
    let es: EventSource | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let backoff = 1000;
    let stopped = false;

    const onChange = () => revalidate();
    const open = () => {
      if (stopped) return;
      es = new EventSource("/api/v1/events");
      es.addEventListener("board.changed", onChange);
      es.onopen = () => {
        backoff = 1000;
      };
      es.onerror = () => {
        es?.removeEventListener("board.changed", onChange);
        es?.close();
        es = null;
        if (stopped) return;
        api("/api/v1/me").then(
          () => schedule(),
          (e) => {
            if (!(e instanceof ApiError && e.status === 401)) schedule();
          },
        );
      };
    };
    const schedule = () => {
      if (stopped) return;
      timer = setTimeout(() => {
        revalidate(); // catch up on changes missed while disconnected
        open();
      }, backoff);
      backoff = Math.min(backoff * 2, maxBackoffMs);
    };

    open();
    return () => {
      stopped = true;
      clearTimeout(timer);
      es?.removeEventListener("board.changed", onChange);
      es?.close();
    };
  }, []);
}
