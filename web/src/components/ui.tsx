import { useEffect } from "react";
import type { ReactNode } from "react";
import type { AppStatus, SystemStatus } from "../lib/pb";

export function StatusPill({ status }: { status: AppStatus | SystemStatus }) {
  const styles: Record<string, string> = {
    running: "bg-emerald-500/15 text-emerald-400",
    online: "bg-emerald-500/15 text-emerald-400",
    stopped: "bg-zinc-500/15 text-zinc-400",
    offline: "bg-zinc-500/15 text-zinc-400",
    paused: "bg-zinc-500/15 text-zinc-400",
    starting: "bg-amber-500/15 text-amber-400",
    backoff: "bg-amber-500/15 text-amber-400",
    errored: "bg-red-500/15 text-red-400",
    unknown: "bg-zinc-500/15 text-zinc-500",
  };
  return (
    <span className={`inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ${styles[status] ?? styles.unknown}`}>
      <span className={`h-1.5 w-1.5 rounded-full ${status === "running" || status === "online" ? "bg-emerald-400" : status === "starting" || status === "backoff" ? "bg-amber-400 animate-pulse" : status === "errored" ? "bg-red-400" : "bg-zinc-500"}`} />
      {status}
    </span>
  );
}

export function Toggle({
  on,
  busy,
  onChange,
  disabled,
  title,
}: {
  on: boolean;
  busy?: boolean;
  disabled?: boolean;
  title?: string;
  onChange: (next: boolean) => void;
}) {
  return (
    <button
      role="switch"
      aria-checked={on}
      title={title}
      disabled={disabled || busy}
      onClick={() => onChange(!on)}
      className={`relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors disabled:opacity-40 ${
        on ? "bg-emerald-500" : "bg-zinc-700"
      } ${busy ? "animate-pulse" : ""}`}
    >
      <span
        className={`inline-block h-4 w-4 rounded-full bg-white transition-transform ${on ? "translate-x-6" : "translate-x-1"}`}
      />
    </button>
  );
}

export function Modal({
  title,
  onClose,
  children,
  wide,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  wide?: boolean;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" onClick={onClose}>
      <div
        className={`w-full ${wide ? "max-w-2xl" : "max-w-md"} rounded-xl border border-zinc-800 bg-zinc-900 p-5 shadow-2xl max-h-[85vh] overflow-y-auto`}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="mb-4 flex items-center justify-between">
          <h2 className="text-lg font-semibold">{title}</h2>
          <button onClick={onClose} className="text-zinc-500 hover:text-zinc-200">✕</button>
        </div>
        {children}
      </div>
    </div>
  );
}

export function CopyBlock({ text }: { text: string }) {
  return (
    <div className="relative">
      <pre className="rounded-lg bg-zinc-950 border border-zinc-800 p-3 text-xs overflow-x-auto whitespace-pre-wrap break-all">{text}</pre>
      <button
        onClick={() => navigator.clipboard.writeText(text)}
        className="absolute right-2 top-2 rounded bg-zinc-800 px-2 py-1 text-xs hover:bg-zinc-700"
      >
        Copy
      </button>
    </div>
  );
}
