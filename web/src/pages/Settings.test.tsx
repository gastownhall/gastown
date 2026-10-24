import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderAt } from "../test/render";
import { mockFetch } from "../test/mocks";

describe("Settings", () => {
  it("shows the gtb join command with the current origin", async () => {
    const calls = mockFetch({
      "GET /api/v1/me": { body: { login: "admin", csrf_token: "tok" } },
      "POST /api/v1/enroll-codes": { body: { code: "ABCD-EFGH", expires_at: new Date(Date.now() + 15 * 60_000).toISOString() } },
    });
    renderAt("/settings");
    await userEvent.click(await screen.findByRole("button", { name: "New enrollment code" }));
    expect(await screen.findByText("ABCD-EFGH")).toBeInTheDocument();
    expect(screen.getByText(`gtb join ${window.location.origin} --code ABCD-EFGH`)).toBeInTheDocument();
    expect(screen.getByText(/expires in 1[45]:\d\d/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")!.headers.get("X-CSRF-Token")).toBe("tok");
  });

  it("falls back when the clipboard is denied", async () => {
    mockFetch({
      "GET /api/v1/me": { body: { login: "admin", csrf_token: "tok" } },
      "POST /api/v1/enroll-codes": { body: { code: "ZZZZ", expires_at: new Date(Date.now() + 60_000).toISOString() } },
    });
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: () => Promise.reject(new Error("denied")) } });
    renderAt("/settings");
    await userEvent.click(await screen.findByRole("button", { name: "New enrollment code" }));
    await userEvent.click(await screen.findByRole("button", { name: "Copy join command" }));
    expect(await screen.findByText(/Copy failed; select the text and copy it manually/)).toBeInTheDocument();
  });
});
