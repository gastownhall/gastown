import { describe, expect, it } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { TerminalView } from "./TerminalView";
import { mockFetch, type Reply } from "../test/mocks";

describe("TerminalView", () => {
  it("drops a peek that answers after the item changed and aborts it", async () => {
    let release!: (r: Reply) => void;
    const late = new Promise<Reply>((r) => (release = r));
    const calls = mockFetch({
      "GET /api/v1/items/m1/gt-1/terminal": () => late,
      "GET /api/v1/items/m1/gt-2/terminal": { body: { lines: "gt-2 peek" } },
    });
    const { rerender } = render(<TerminalView machine="m1" itemId="gt-1" />);
    await waitFor(() => expect(calls.some((c) => c.path === "/api/v1/items/m1/gt-1/terminal")).toBe(true));
    rerender(<TerminalView machine="m1" itemId="gt-2" />);
    expect(await screen.findByText("gt-2 peek")).toBeInTheDocument();
    await act(async () => {
      release({ body: { lines: "gt-1 peek" } });
      await new Promise((r) => setTimeout(r, 30));
    });
    expect(screen.queryByText("gt-1 peek")).toBeNull();
    expect(screen.getByText("gt-2 peek")).toBeInTheDocument();
    expect(calls.find((c) => c.path === "/api/v1/items/m1/gt-1/terminal")!.signal?.aborted).toBe(true);
  });

  it("aborts the in-flight peek on unmount", async () => {
    const calls = mockFetch({ "GET /api/v1/items/m1/gt-1/terminal": () => new Promise<Reply>(() => {}) });
    const { unmount } = render(<TerminalView machine="m1" itemId="gt-1" />);
    await waitFor(() => expect(calls).toHaveLength(1));
    unmount();
    expect(calls[0].signal?.aborted).toBe(true);
  });
});
