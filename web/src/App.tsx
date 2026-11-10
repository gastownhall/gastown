import { Link, NavLink, Outlet, type RouteObject } from "react-router";
import { api, useApi, type UseApiOptions } from "./api";
import { useBoardEvents } from "./events";
import { ToastProvider, useToast } from "./components/Toast";
import type { Machine } from "./types";
import { Board } from "./pages/Board";
import { Item } from "./pages/Item";
import { Machines } from "./pages/Machines";
import { Coordination } from "./pages/Coordination";
import { Settings } from "./pages/Settings";

// useMachines is the machine list, shared by pages for names and filters.
export function useMachines(opts?: UseApiOptions) {
  return useApi<Machine[]>("/api/v1/machines", opts);
}

export function machineNames(ms: Machine[] | undefined): Record<string, string> {
  const out: Record<string, string> = {};
  for (const m of ms ?? []) out[m.id] = m.hostname || m.id;
  return out;
}

const nav = [
  { to: "/", label: "Board", end: true },
  { to: "/machines", label: "Machines" },
  { to: "/coordination", label: "Coordination" },
  { to: "/settings", label: "Settings" },
];

function LogoutButton() {
  const toast = useToast();
  async function logout() {
    try {
      await api("/auth/logout", { method: "POST" });
    } catch (e) {
      toast(`Logout failed: ${String(e)}`);
      return;
    }
    window.location.assign("/login");
  }
  return (
    <button
      type="button"
      onClick={logout}
      className="w-full rounded-md px-3 py-2 text-left text-sm text-zinc-600 hover:bg-zinc-200 focus-visible:outline-2 focus-visible:outline-teal-500 dark:text-zinc-400 dark:hover:bg-zinc-800"
    >
      Log out
    </button>
  );
}

function Layout() {
  useBoardEvents();
  return (
    <div className="flex h-full min-h-screen">
      <aside className="flex w-48 shrink-0 flex-col border-r border-zinc-200 bg-zinc-50 p-3 dark:border-zinc-800 dark:bg-zinc-900">
        <div className="mb-6 px-3 pt-1 font-mono text-lg font-semibold tracking-tight text-teal-700 dark:text-teal-400">gtb</div>
        <nav aria-label="Main" className="flex flex-col gap-1">
          {nav.map((n) => (
            <NavLink
              key={n.to}
              to={n.to}
              end={n.end}
              className={({ isActive }) =>
                `rounded-md px-3 py-2 text-sm focus-visible:outline-2 focus-visible:outline-teal-500 ${
                  isActive
                    ? "bg-zinc-200 font-medium text-zinc-900 dark:bg-zinc-800 dark:text-zinc-100"
                    : "text-zinc-600 hover:bg-zinc-200 dark:text-zinc-400 dark:hover:bg-zinc-800"
                }`
              }
            >
              {n.label}
            </NavLink>
          ))}
        </nav>
        <div className="mt-auto">
          <LogoutButton />
        </div>
      </aside>
      <main className="min-w-0 flex-1 overflow-auto">
        <Outlet />
      </main>
    </div>
  );
}

function Root() {
  return (
    <ToastProvider>
      <Layout />
    </ToastProvider>
  );
}

// NotFound is the catch-all page and the root errorElement, so users never
// see React Router's developer error screen.
export function NotFound() {
  return (
    <div className="flex min-h-screen flex-col items-center justify-center gap-3 bg-white p-6 text-zinc-900 dark:bg-zinc-950 dark:text-zinc-100">
      <h1 className="text-lg font-semibold">Not found</h1>
      <Link to="/" className="text-sm text-teal-700 underline focus-visible:outline-2 focus-visible:outline-teal-500 dark:text-teal-400">
        Back to the board
      </Link>
    </div>
  );
}

export const routes: RouteObject[] = [
  {
    path: "/",
    element: <Root />,
    errorElement: <NotFound />,
    children: [
      { index: true, element: <Board /> },
      { path: "items/:machine/:id", element: <Item /> },
      { path: "machines", element: <Machines /> },
      { path: "coordination", element: <Coordination /> },
      { path: "settings", element: <Settings /> },
      { path: "*", element: <NotFound /> },
    ],
  },
];
