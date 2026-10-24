import { describe, expect, it, vi } from "vitest";
import { act, screen, within, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderAt } from "../test/render";
import { item, mockFetch, MockEventSource } from "../test/mocks";

const machines = [
  { id: "m1", hostname: "box-one", os: "linux", arch: "amd64", gtb_version: "0.1.0", protocol: "1", created_at: "2026-10-01T00:00:00Z", last_seen_at: "2026-10-22T10:00:00Z", revoked_at: null, freshness_seconds: 3, stale: false, rigs: [{ name: "core", remote: "", gated: true }] },
];

function setup(board: unknown) {
  return mockFetch({
    "GET /api/v1/me": { body: { login: "admin", csrf_token: "tok" } },
    "GET /api/v1/machines": { body: machines },
    "GET /api/v1/board": { body: board },
  });
}

describe("Board", () => {
  it("puts Needs you first with its empty state, and marks stale cards", async () => {
    setup({ lanes: [{ step: "spec", items: [item({ id: "gt-7", stale: true, freshness_seconds: 999 })] }, { step: "plan", items: [item({ id: "gt-8", step: "plan", freshness_seconds: 100 })] }], needs_you: [] });
    renderAt("/");
    const headings = await screen.findAllByRole("heading", { level: 2 });
    expect(headings.map((h) => h.textContent?.replace(/\d+$/, "").trim())).toEqual(["Needs you", "spec", "plan"]);
    expect(screen.getByText("Nothing waiting on you")).toBeInTheDocument();
    const stale = screen.getByRole("link", { name: /gt-7/ });
    expect(within(stale).getByText("stale")).toBeInTheDocument();
    const fresh = screen.getByRole("link", { name: /gt-8/ });
    expect(within(fresh).getByText("1m ago")).toBeInTheDocument();
  });

  it("shows waiting reasons on Needs you cards and links cards to the item page", async () => {
    const waiting = item({ id: "gt-9", waiting_for_human: "approve spec" });
    setup({ lanes: [{ step: "spec", items: [waiting] }], needs_you: [waiting] });
    const { router } = renderAt("/");
    const lane = await screen.findByRole("region", { name: "Needs you" });
    expect(within(lane).getByText("approve spec")).toBeInTheDocument();
    await userEvent.click(within(lane).getByRole("link", { name: /gt-9/ }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/items/m1/gt-9"));
  });

  it("writes filters to the URL and refetches the board with them", async () => {
    const calls = setup({ lanes: [], needs_you: [] });
    const { router } = renderAt("/");
    await screen.findByRole("region", { name: "Needs you" });
    const machine = await screen.findByLabelText("Machine");
    await waitFor(() => expect(within(machine).getByRole("option", { name: "box-one" })).toBeInTheDocument());
    await userEvent.selectOptions(machine, "m1");
    await userEvent.selectOptions(screen.getByLabelText("Kind"), "fix");
    await userEvent.selectOptions(screen.getByLabelText("Rig"), "core");
    await waitFor(() => {
      const q = new URLSearchParams(router.state.location.search);
      expect([q.get("machine"), q.get("kind"), q.get("rig")]).toEqual(["m1", "fix", "core"]);
    });
    await waitFor(() => {
      const last = calls.filter((c) => c.path === "/api/v1/board").at(-1)!;
      const lq = new URLSearchParams(last.search);
      expect([lq.get("machine"), lq.get("kind"), lq.get("rig")]).toEqual(["m1", "fix", "core"]);
    });
  });

  it("revalidates on board.changed", async () => {
    const calls = setup({ lanes: [], needs_you: [] });
    renderAt("/");
    await screen.findByRole("region", { name: "Needs you" });
    const before = calls.filter((c) => c.path === "/api/v1/board").length;
    expect(MockEventSource.instances).toHaveLength(1);
    expect(MockEventSource.instances[0].url).toBe("/api/v1/events");
    MockEventSource.instances[0].emit("board.changed");
    await waitFor(() => expect(calls.filter((c) => c.path === "/api/v1/board").length).toBeGreaterThan(before));
  });

  describe("client-side freshness (R44)", () => {
    it("turns a 110s card stale after 15s without a refetch, and revalidates every 60s", async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const calls = setup({ lanes: [{ step: "spec", items: [item({ id: "gt-5", freshness_seconds: 110, stale: false })] }], needs_you: [] });
      renderAt("/");
      const card = await screen.findByRole("link", { name: /gt-5/ });
      expect(within(card).getByText("1m ago")).toBeInTheDocument();
      const boardCalls = () => calls.filter((c) => c.path === "/api/v1/board").length;
      const before = boardCalls();

      await act(async () => {
        vi.advanceTimersByTime(15_000);
      });
      expect(within(screen.getByRole("link", { name: /gt-5/ })).getByText("stale")).toBeInTheDocument();
      expect(boardCalls()).toBe(before);

      await act(async () => {
        vi.advanceTimersByTime(45_000);
      });
      await waitFor(() => expect(boardCalls()).toBeGreaterThan(before));
    });
  });
});
