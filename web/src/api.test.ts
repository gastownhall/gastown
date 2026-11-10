import { describe, expect, it, vi } from "vitest";
import { api, ApiError, loginRedirect } from "./api";
import { mockFetch } from "./test/mocks";

describe("api", () => {
  it("redirects to /login on 401", async () => {
    const go = vi.spyOn(loginRedirect, "go").mockImplementation(() => {});
    mockFetch({ "GET /api/v1/board": { status: 401, body: { error: "unauthorized" } } });
    await expect(api("/api/v1/board")).rejects.toBeInstanceOf(ApiError);
    expect(go).toHaveBeenCalledWith("/login");
  });

  it("sends the CSRF header on POST and DELETE but not on GET", async () => {
    const calls = mockFetch({
      "GET /api/v1/me": { body: { login: "admin", csrf_token: "tok-1" } },
      "GET /api/v1/machines": { body: [] },
      "POST /api/v1/enroll-codes": { body: { code: "C", expires_at: "2030-01-01T00:00:00Z" } },
      "DELETE /api/v1/machines/m1": { body: { ok: true } },
    });
    await api("/api/v1/machines");
    await api("/api/v1/enroll-codes", { method: "POST" });
    await api("/api/v1/machines/m1", { method: "DELETE" });

    const get = calls.find((c) => c.path === "/api/v1/machines" && c.method === "GET")!;
    expect(get.headers.get("X-CSRF-Token")).toBeNull();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.headers.get("X-CSRF-Token")).toBe("tok-1");
    expect(post.credentials).toBe("same-origin");
    const del = calls.find((c) => c.method === "DELETE")!;
    expect(del.headers.get("X-CSRF-Token")).toBe("tok-1");
    for (const c of calls) expect(c.credentials).toBe("same-origin");
  });

  it("turns JSON errors into ApiError with the server's code", async () => {
    mockFetch({ "GET /api/v1/items/m1/x": { status: 404, body: { error: "item_not_found" } } });
    await expect(api("/api/v1/items/m1/x")).rejects.toMatchObject({ status: 404, code: "item_not_found" });
  });
});
