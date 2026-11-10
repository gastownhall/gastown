import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router";
import { routes } from "./App";
import { getMe } from "./api";
import "./index.css";

// Probe the session first: a 401 redirects to the server's /login form.
void getMe().catch(() => {});

const router = createBrowserRouter(routes);
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
);
