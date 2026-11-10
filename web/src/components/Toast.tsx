// A minimal toast stack for errors and confirmations.
import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";

type Kind = "error" | "info";
interface Toast {
  id: number;
  text: string;
  kind: Kind;
}

const Ctx = createContext<(text: string, kind?: Kind) => void>(() => {});

export function useToast() {
  return useContext(Ctx);
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(1);
  const dismiss = useCallback((id: number) => setToasts((ts) => ts.filter((t) => t.id !== id)), []);
  const show = useCallback(
    (text: string, kind: Kind = "error") => {
      const id = next.current++;
      setToasts((ts) => [...ts.slice(-3), { id, text, kind }]);
      setTimeout(() => dismiss(id), 6000);
    },
    [dismiss],
  );
  const value = useMemo(() => show, [show]);
  return (
    <Ctx.Provider value={value}>
      {children}
      <div aria-live="polite" className="pointer-events-none fixed right-4 bottom-4 z-50 flex w-80 flex-col gap-2">
        {toasts.map((t) => (
          <div
            key={t.id}
            role={t.kind === "error" ? "alert" : "status"}
            className={`pointer-events-auto flex items-start gap-2 rounded-md border px-3 py-2 text-sm shadow-lg ${
              t.kind === "error"
                ? "border-red-300 bg-red-50 text-red-800 dark:border-red-800 dark:bg-red-950 dark:text-red-200"
                : "border-zinc-300 bg-white text-zinc-800 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-200"
            }`}
          >
            <span className="flex-1">{t.text}</span>
            <button type="button" aria-label="Dismiss" className="rounded px-1 focus-visible:outline-2 focus-visible:outline-teal-500" onClick={() => dismiss(t.id)}>
              ×
            </button>
          </div>
        ))}
      </div>
    </Ctx.Provider>
  );
}
