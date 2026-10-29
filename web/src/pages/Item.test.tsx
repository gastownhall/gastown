import { describe, expect, it, vi } from "vitest";
import { act, screen, waitFor } from "@testing-library/react";
import { timing } from "../api";
import userEvent from "@testing-library/user-event";
import { renderAt } from "../test/render";
import { hideable, item, mockFetch, MockEventSource, SHA, type Handler, type Reply } from "../test/mocks";

function detail() {
  return {
    item: item({ id: "gt-1", step: "spec", waiting_for_human: "approve spec" }),
    artifacts: [{ name: "spec", sha: SHA, content: "# The spec\n\nBody text." }],
    events: [
      { id: "2", at: "2026-10-22T10:05:00Z", type: "evidence", item_id: "gt-1", data: { kind: "tdd", phase: "red", cmd: "go test ./...", exit: 1, tail: "FAIL x" } },
      { id: "1", at: "2026-10-22T10:00:00Z", type: "item.upsert", item_id: "gt-1", data: { step: "spec" } },
    ],
    diffstat: [{ path: "a.go", added: 3, deleted: 1 }],
  };
}

function setup(result: unknown, extra: Record<string, Handler | Reply> = {}) {
  return mockFetch({
    "GET /api/v1/me": { body: { login: "admin", csrf_token: "tok-9" } },
    "GET /api/v1/machines": { body: [] },
    "GET /api/v1/items/m1/gt-1": { body: detail() },
    "POST /api/v1/items/m1/gt-1/approve": { status: 202, body: { key: "k1", status: "pending" } },
    "POST /api/v1/items/m1/gt-1/request-changes": { status: 202, body: { key: "k2", status: "pending" } },
    "GET /api/v1/requests/m1/k1": { body: result },
    "GET /api/v1/requests/m1/k2": { body: result },
    ...extra,
  });
}

describe("Item", () => {
  it("approves with the sha being viewed and the CSRF header", async () => {
    const calls = setup({ status: "done", result: { ok: true, error: "" } });
    renderAt("/items/m1/gt-1?tab=artifacts");
    expect(await screen.findByText(SHA.slice(0, 12))).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.path).toBe("/api/v1/items/m1/gt-1/approve");
    expect(post.body).toEqual({ artifact: "spec", sha: SHA });
    expect(post.headers.get("X-CSRF-Token")).toBe("tok-9");
    expect(await screen.findByText("Approved")).toBeInTheDocument();
  });

  it("shows the reload message on a stale_sha result", async () => {
    setup({ status: "done", result: { ok: false, error: "stale_sha" } });
    renderAt("/items/m1/gt-1?tab=artifacts");
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Artifact changed on the machine; reload to review the new version")).toBeInTheDocument();
  });

  it("shows the expired message on an expired request", async () => {
    setup({ status: "expired", result: null });
    renderAt("/items/m1/gt-1?tab=artifacts");
    await userEvent.click(await screen.findByRole("button", { name: "Request changes" }));
    expect(await screen.findByText("Request expired; the machine was offline")).toBeInTheDocument();
  });

  it("maps a machine-reported expired error to the expired message", async () => {
    setup({ status: "done", result: { ok: false, error: "expired" } });
    renderAt("/items/m1/gt-1?tab=artifacts");
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Request expired; the machine was offline")).toBeInTheDocument();
  });

  it("disables the buttons while pending and shows waiting", async () => {
    setup({ status: "delivered", result: null });
    renderAt("/items/m1/gt-1?tab=artifacts");
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Waiting for machine…")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Request changes" })).toBeDisabled();
  });

  it("shows other machine errors verbatim", async () => {
    setup({ status: "done", result: { ok: false, error: "gate closed" } });
    renderAt("/items/m1/gt-1?tab=artifacts");
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Machine reported: gate closed")).toBeInTheDocument();
  });

  it("refreshes the terminal tab", async () => {
    let n = 0;
    const calls = setup(null, {
      "GET /api/v1/items/m1/gt-1/terminal": () => ({ body: { lines: n++ === 0 ? "first peek" : "second peek" } }),
    });
    renderAt("/items/m1/gt-1?tab=terminal");
    expect(await screen.findByText("first peek")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Refresh terminal" }));
    expect(await screen.findByText("second peek")).toBeInTheDocument();
    expect(calls.filter((c) => c.path.endsWith("/terminal")).length).toBe(2);
  });

  it("auto-refreshes the terminal while visible and stops when hidden", async () => {
    timing.terminalMs = 30;
    let vis: DocumentVisibilityState = "visible";
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => vis });
    let n = 0;
    const calls = setup(null, { "GET /api/v1/items/m1/gt-1/terminal": () => ({ body: { lines: `peek ${++n}` } }) });
    renderAt("/items/m1/gt-1?tab=terminal");
    expect(await screen.findByText("peek 3")).toBeInTheDocument();
    vis = "hidden";
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    await new Promise((r) => setTimeout(r, 40));
    const stopped = calls.filter((c) => c.path.endsWith("/terminal")).length;
    await new Promise((r) => setTimeout(r, 120));
    expect(calls.filter((c) => c.path.endsWith("/terminal")).length).toBe(stopped);
    vis = "visible";
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    await waitFor(() => expect(calls.filter((c) => c.path.endsWith("/terminal")).length).toBeGreaterThan(stopped));
    // @ts-expect-error restore the jsdom getter
    delete document.visibilityState;
  });

  it("reports a machine that does not answer the terminal peek", async () => {
    setup(null, { "GET /api/v1/items/m1/gt-1/terminal": { status: 504, body: { error: "machine did not answer" } } });
    renderAt("/items/m1/gt-1?tab=terminal");
    expect(await screen.findByText(/machine did not answer/)).toBeInTheDocument();
  });

  it("keeps the tab in the URL and renders tablist", async () => {
    setup(null);
    const { router } = renderAt("/items/m1/gt-1");
    const tablist = await screen.findByRole("tablist");
    expect(tablist).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Files" }));
    await waitFor(() => expect(new URLSearchParams(router.state.location.search).get("tab")).toBe("files"));
    expect(await screen.findByText("a.go")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Evidence" }));
    expect(await screen.findByText("FAIL x")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Usage" }));
    expect(await screen.findByText("No usage reported")).toBeInTheDocument();
  });

  it("renders artifact markdown without executing injected HTML", async () => {
    const d = detail();
    d.artifacts[0].content = "# Title\n\n<img src=x onerror=alert(1)><script>alert(2)</script>";
    setup(null, { "GET /api/v1/items/m1/gt-1": { body: d } });
    const { container } = renderAt("/items/m1/gt-1?tab=artifacts");
    expect(await screen.findByRole("heading", { name: "Title" })).toBeInTheDocument();
    await waitFor(() => expect(container.querySelector("script")).toBeNull());
    expect(container.querySelector("[onerror]")).toBeNull();
  });

  describe("pinned artifact (R43)", () => {
    const B = "c".repeat(64);
    const banner = "A newer version of spec is on the machine.";

    // The first item fetch has spec at SHA with "Version A"; later fetches
    // have it at B with "Version B".
    function setupVersions() {
      let n = 0;
      return setup({ status: "done", result: { ok: false, error: "stale_sha" } }, {
        "GET /api/v1/items/m1/gt-1": () => {
          const d = detail();
          d.artifacts[0] = n++ === 0 ? { name: "spec", sha: SHA, content: "Version A" } : { name: "spec", sha: B, content: "Version B" };
          return { body: d };
        },
      });
    }

    async function openAndRefetch() {
      renderAt("/items/m1/gt-1?tab=artifacts");
      expect(await screen.findByText("Version A")).toBeInTheDocument();
      act(() => MockEventSource.instances[0].emit("board.changed"));
      expect(await screen.findByText(banner)).toBeInTheDocument();
    }

    it("keeps the opened version and approves its sha after a refetch brings a newer one", async () => {
      const calls = setupVersions();
      await openAndRefetch();
      expect(screen.getByText("Version A")).toBeInTheDocument();
      expect(screen.queryByText("Version B")).toBeNull();
      expect(screen.getByText(SHA.slice(0, 12))).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Approve" }));
      expect(calls.find((c) => c.method === "POST")!.body).toEqual({ artifact: "spec", sha: SHA });
      expect(await screen.findByText("Artifact changed on the machine; reload to review the new version")).toBeInTheDocument();
      expect(screen.getByText(banner)).toBeInTheDocument();
    });

    it("re-pins to the latest version on Load it", async () => {
      const calls = setupVersions();
      await openAndRefetch();
      await userEvent.click(screen.getByRole("button", { name: "Load it" }));
      expect(await screen.findByText("Version B")).toBeInTheDocument();
      expect(screen.queryByText(banner)).toBeNull();
      await userEvent.click(screen.getByRole("button", { name: "Approve" }));
      expect(calls.find((c) => c.method === "POST")!.body).toEqual({ artifact: "spec", sha: B });
    });

    it("disables approval when the pinned artifact was deleted on the machine", async () => {
      let n = 0;
      const calls = setup(null, {
        "GET /api/v1/items/m1/gt-1": () => {
          const d = detail();
          d.artifacts = n++ === 0 ? [{ name: "spec", sha: SHA, content: "Version A" }, { name: "plan", sha: B, content: "The plan" }] : [{ name: "plan", sha: B, content: "The plan" }];
          return { body: d };
        },
      });
      renderAt("/items/m1/gt-1?tab=artifacts");
      expect(await screen.findByText("Version A")).toBeInTheDocument();
      act(() => MockEventSource.instances[0].emit("board.changed"));
      expect(await screen.findByText("spec is no longer on the machine.")).toBeInTheDocument();
      expect(screen.getByText("Version A")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
      expect(screen.getByRole("button", { name: "Request changes" })).toBeDisabled();
      expect(calls.some((c) => c.method === "POST")).toBe(false);
    });
  });

  describe("backstop revalidation (R45)", () => {
    const itemFetches = (calls: ReturnType<typeof setup>) => calls.filter((c) => c.path === "/api/v1/items/m1/gt-1").length;
    const advance = (ms: number) =>
      act(async () => {
        vi.advanceTimersByTime(ms);
      });

    it("refetches the item every 60 s with no SSE", async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const calls = setup(null);
      renderAt("/items/m1/gt-1");
      await screen.findByRole("tablist");
      expect(itemFetches(calls)).toBe(1);
      await advance(59_000);
      expect(itemFetches(calls)).toBe(1);
      await advance(1_000);
      await waitFor(() => expect(itemFetches(calls)).toBe(2));
    });

    it("does not refetch while the page is hidden, and catches up when shown", async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const vis = hideable();
      const calls = setup(null);
      renderAt("/items/m1/gt-1");
      await screen.findByRole("tablist");
      vis.set("hidden");
      await advance(180_000);
      expect(itemFetches(calls)).toBe(1);
      vis.set("visible");
      await waitFor(() => expect(itemFetches(calls)).toBe(2));
      vis.restore();
    });

    it("an SSE revalidate at 50 s moves the next backstop fetch to 110 s", async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const calls = setup(null);
      renderAt("/items/m1/gt-1");
      await screen.findByRole("tablist");
      await advance(50_000);
      act(() => MockEventSource.instances[0].emit("board.changed"));
      await waitFor(() => expect(itemFetches(calls)).toBe(2));
      await advance(10_000); // t = 60 s: no backstop fetch
      expect(itemFetches(calls)).toBe(2);
      await advance(49_000); // t = 109 s
      expect(itemFetches(calls)).toBe(2);
      await advance(1_000); // t = 110 s
      await waitFor(() => expect(itemFetches(calls)).toBe(3));
    });
  });
});
