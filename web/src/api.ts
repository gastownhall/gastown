// A tiny fetch wrapper for the gtb browser API: same-origin credentials, the
// session's X-CSRF-Token on mutating calls, a redirect to /login on 401, and
// JSON errors as ApiError. Also a revalidation bus and a data hook.
import { useCallback, useEffect, useState } from "react";
import type { Me, RequestStatus } from "./types";

// Poll and refresh intervals. Tests shorten them.
export const timing = { pollMs: 1000, pollMaxMs: 60_000, terminalMs: 5000 };

// loginRedirect is indirect so tests can observe it.
export const loginRedirect = {
  go(path: string) {
    window.location.assign(path);
  },
};

export class ApiError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string) {
    super(code || `HTTP ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

let mePromise: Promise<Me> | null = null;
const listeners = new Set<() => void>();

export function resetApiState() {
  mePromise = null;
  listeners.clear();
}

// getMe returns the session (cached). A 401 redirects to /login.
export function getMe(): Promise<Me> {
  if (!mePromise) {
    mePromise = request<Me>("/api/v1/me", { method: "GET" }, null).catch((e) => {
      mePromise = null;
      throw e;
    });
  }
  return mePromise;
}

function isMutating(method: string) {
  return method !== "GET" && method !== "HEAD";
}

async function request<T>(path: string, init: RequestInit, csrf: string | null): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  if (csrf) headers.set("X-CSRF-Token", csrf);
  let res: Response;
  try {
    res = await fetch(path, { ...init, headers, credentials: "same-origin" });
  } catch {
    throw new ApiError(0, "network error");
  }
  if (res.status === 401) {
    loginRedirect.go("/login");
    throw new ApiError(401, "unauthorized");
  }
  const text = await res.text();
  let body: unknown = undefined;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = undefined;
    }
  }
  if (!res.ok) {
    const code = body && typeof body === "object" && "error" in body ? String((body as { error: unknown }).error) : "";
    throw new ApiError(res.status, code || `HTTP ${res.status}`);
  }
  return body as T;
}

export interface ApiInit {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
}

// api calls path. Mutating methods carry the CSRF token; a csrf refusal
// (token rotated) refetches /me and retries once.
export async function api<T = unknown>(path: string, opts: ApiInit = {}): Promise<T> {
  const method = (opts.method ?? "GET").toUpperCase();
  const init: RequestInit = { method, signal: opts.signal };
  if (opts.body !== undefined) init.body = JSON.stringify(opts.body);
  if (!isMutating(method)) return request<T>(path, init, null);
  const me = await getMe();
  try {
    return await request<T>(path, init, me.csrf_token);
  } catch (e) {
    if (e instanceof ApiError && e.status === 403 && e.code === "csrf") {
      mePromise = null;
      const fresh = await getMe();
      return request<T>(path, init, fresh.csrf_token);
    }
    throw e;
  }
}

// errorText is the user-facing text for a failed call.
export function errorText(e: unknown): string {
  if (e instanceof ApiError) return e.code.replace(/_/g, " ");
  if (e instanceof Error) return e.message;
  return String(e);
}

// revalidate asks every mounted useApi hook to refetch.
export function revalidate() {
  for (const fn of [...listeners]) fn();
}

export function onRevalidate(fn: () => void): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

interface ApiState<T> {
  key: string | null;
  data?: T;
  // fetchedAt is when data arrived (Date.now()).
  fetchedAt?: number;
  error?: unknown;
  loading: boolean;
}

export interface UseApiOptions {
  // refreshInterval (ms) refetches as a backstop for missed SSE events. The
  // timer restarts after every fetch, whatever triggered it (SSE, manual or
  // the timer), pauses while the page is hidden, and on show fetches at once
  // if a full interval has passed or else waits out the rest.
  refreshInterval?: number;
}

// BACKSTOP_MS is the backstop refetch interval for live pages.
export const BACKSTOP_MS = 60_000;

// useApi fetches path (null: nothing) and refetches it on revalidate().
// Data for an earlier path is not returned for a new one.
export function useApi<T>(path: string | null, opts: UseApiOptions = {}) {
  const { refreshInterval } = opts;
  const [state, setState] = useState<ApiState<T>>({ key: null, loading: path !== null });
  const [tick, setTick] = useState(0);
  const reload = useCallback(() => setTick((t) => t + 1), []);
  useEffect(() => onRevalidate(reload), [reload]);
  useEffect(() => {
    if (path === null) return;
    let live = true;
    const ctl = new AbortController();
    setState((s) => (s.key === path ? { ...s, loading: true } : { key: path, loading: true }));
    api<T>(path, { signal: ctl.signal }).then(
      (data) => live && setState({ key: path, data, fetchedAt: Date.now(), loading: false }),
      (error) =>
        live &&
        setState((s) => (s.key === path ? { ...s, error, loading: false } : { key: path, error, loading: false })),
    );
    return () => {
      live = false;
      ctl.abort();
    };
  }, [path, tick]);
  useEffect(() => {
    if (path === null || !refreshInterval) return;
    const fetchedAt = Date.now(); // this effect reruns with every fetch (tick)
    let timer: ReturnType<typeof setTimeout> | undefined;
    const arm = (ms: number) => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        if (!document.hidden) reload();
      }, ms);
    };
    const onVis = () => {
      if (document.hidden) {
        clearTimeout(timer);
        return;
      }
      const left = refreshInterval - (Date.now() - fetchedAt);
      if (left <= 0) reload();
      else arm(left);
    };
    if (!document.hidden) arm(refreshInterval);
    document.addEventListener("visibilitychange", onVis);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [path, tick, refreshInterval, reload]);
  const mine = state.key === path;
  return {
    data: mine ? state.data : undefined,
    fetchedAt: mine ? state.fetchedAt : undefined,
    error: mine ? state.error : undefined,
    loading: !mine || state.loading,
    reload,
  };
}

export type Outcome =
  | { kind: "done"; ok: true; data?: unknown }
  | { kind: "done"; ok: false; error: string; data?: unknown }
  | { kind: "expired" }
  | { kind: "timeout" };

const sleep = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const t = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => {
      clearTimeout(t);
      reject(new DOMException("aborted", "AbortError"));
    });
  });

// pollRequest polls GET /requests/{machine}/{key} every timing.pollMs until
// the request is done or expired, or timing.pollMaxMs passes.
export async function pollRequest(machine: string, key: string, signal?: AbortSignal): Promise<Outcome> {
  const deadline = Date.now() + timing.pollMaxMs;
  const path = `/api/v1/requests/${encodeURIComponent(machine)}/${encodeURIComponent(key)}`;
  for (;;) {
    const st = await api<RequestStatus>(path, { signal });
    if (st.status === "done" && st.result) {
      return st.result.ok
        ? { kind: "done", ok: true, data: st.result.data }
        : { kind: "done", ok: false, error: st.result.error, data: st.result.data };
    }
    if (st.status === "expired") return { kind: "expired" };
    if (Date.now() + timing.pollMs > deadline) return { kind: "timeout" };
    await sleep(timing.pollMs, signal);
  }
}

export function itemPath(machine: string, id: string) {
  return `/api/v1/items/${encodeURIComponent(machine)}/${encodeURIComponent(id)}`;
}
