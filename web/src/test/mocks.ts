import { vi } from "vitest";

export interface Reply {
  status?: number;
  body?: unknown;
}

export type Handler = (req: { url: URL; method: string; headers: Headers; body: unknown }) => Reply | Promise<Reply>;

export interface Call {
  method: string;
  path: string;
  search: string;
  headers: Headers;
  body: unknown;
  credentials?: RequestCredentials;
  signal?: AbortSignal | null;
}

// mockFetch installs a fetch stub that routes "METHOD /path" (no query) to
// handlers. Unknown routes answer 404 {"error":"not_found"}. It returns the
// recorded calls.
export function mockFetch(routes: Record<string, Handler | Reply>): Call[] {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input), "http://gtb.test");
    const method = (init.method ?? "GET").toUpperCase();
    const headers = new Headers(init.headers);
    const body = typeof init.body === "string" ? JSON.parse(init.body) : undefined;
    calls.push({ method, path: url.pathname, search: url.search, headers, body, credentials: init.credentials, signal: init.signal });
    const route = routes[`${method} ${url.pathname}`];
    const res = await (typeof route === "function" ? route({ url, method, headers, body }) : route ?? { status: 404, body: { error: "not_found" } });
    return new Response(res.body === undefined ? null : JSON.stringify(res.body), {
      status: res.status ?? 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fn);
  return calls;
}

export class MockEventSource {
  static instances: MockEventSource[] = [];
  url: string;
  onerror: ((e: Event) => void) | null = null;
  onopen: ((e: Event) => void) | null = null;
  closed = false;
  private listeners = new Map<string, Set<(e: MessageEvent) => void>>();
  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }
  addEventListener(type: string, fn: (e: MessageEvent) => void) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set());
    this.listeners.get(type)!.add(fn);
  }
  removeEventListener(type: string, fn: (e: MessageEvent) => void) {
    this.listeners.get(type)?.delete(fn);
  }
  emit(type: string, data = "{}") {
    for (const fn of this.listeners.get(type) ?? []) fn(new MessageEvent(type, { data }));
  }
  close() {
    this.closed = true;
  }
}

export const SHA = "a".repeat(40) + "b".repeat(24);

export function item(over: Record<string, unknown> = {}) {
  return {
    id: "gt-1",
    rig: "core",
    kind: "task",
    step: "spec",
    title: "Add widgets",
    parent: "",
    branch: "feat/widgets",
    waiting_for_human: "",
    depends_on: [],
    step_entered_at: "2026-10-22T10:00:00Z",
    machine_id: "m1",
    updated_at: "2026-10-22T10:00:00Z",
    last_seen_at: "2026-10-22T10:00:00Z",
    freshness_seconds: 5,
    stale: false,
    ...over,
  };
}

// hideable overrides document visibility; set() dispatches visibilitychange.
export function hideable() {
  let state: DocumentVisibilityState = "visible";
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
  Object.defineProperty(document, "hidden", { configurable: true, get: () => state === "hidden" });
  return {
    set(v: DocumentVisibilityState) {
      state = v;
      document.dispatchEvent(new Event("visibilitychange"));
    },
    restore() {
      // @ts-expect-error remove the own-property overrides (jsdom getters remain)
      delete document.visibilityState;
      // @ts-expect-error see above
      delete document.hidden;
    },
  };
}
