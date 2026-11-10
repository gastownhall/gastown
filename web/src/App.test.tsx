import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { createMemoryRouter, RouterProvider, type RouteObject } from "react-router";
import { routes } from "./App";
import { renderAt } from "./test/render";
import { mockFetch } from "./test/mocks";

function Boom(): never {
  throw new Error("boom");
}

describe("App routes", () => {
  it("renders a plain Not found page for unknown paths", async () => {
    mockFetch({});
    renderAt("/no/such/page");
    expect(await screen.findByRole("heading", { name: "Not found" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to the board" })).toHaveAttribute("href", "/");
  });

  it("renders the Not found page instead of the router's error screen", async () => {
    mockFetch({});
    const root = routes[0] as RouteObject & { index?: false; children?: RouteObject[] };
    const withBoom: RouteObject = { ...root, children: [...(root.children ?? []), { path: "boom", element: <Boom /> }] };
    const router = createMemoryRouter([withBoom], { initialEntries: ["/boom"] });
    const quiet = console.error;
    console.error = () => {};
    try {
      render(<RouterProvider router={router} />);
      expect(await screen.findByRole("heading", { name: "Not found" })).toBeInTheDocument();
      expect(screen.queryByText(/Unexpected Application Error/)).toBeNull();
      expect(screen.getByRole("link", { name: "Back to the board" })).toBeInTheDocument();
    } finally {
      console.error = quiet;
    }
  });
});
