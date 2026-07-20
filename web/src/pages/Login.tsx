import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { pb } from "../lib/pb";

export default function Login() {
  const nav = useNavigate();
  const [needsSetup, setNeedsSetup] = useState(false);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    fetch("/api/bb/setup")
      .then((r) => r.json())
      .then((d) => setNeedsSetup(!!d.needs_setup))
      .catch(() => {});
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      if (needsSetup) {
        const r = await fetch("/api/bb/setup", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ email, password }),
        });
        if (!r.ok) throw new Error("setup failed");
      }
      await pb.collection("users").authWithPassword(email, password);
      nav("/");
    } catch {
      setError(needsSetup ? "Setup failed — password must be 8+ characters." : "Invalid email or password.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center px-4">
      <form onSubmit={submit} className="w-full max-w-sm space-y-4">
        <div className="text-center mb-8">
          <div className="text-4xl mb-2">⚡</div>
          <h1 className="text-2xl font-bold tracking-tight">BreakerBox</h1>
          <p className="text-sm text-zinc-400 mt-1">
            {needsSetup ? "Create your admin account to get started" : "Sign in to your hub"}
          </p>
        </div>
        <input
          type="email"
          required
          placeholder="Email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          className="w-full rounded-lg bg-zinc-900 border border-zinc-800 px-3 py-2 text-sm focus:outline-none focus:border-amber-500"
        />
        <input
          type="password"
          required
          placeholder={needsSetup ? "Choose a password (8+ chars)" : "Password"}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          className="w-full rounded-lg bg-zinc-900 border border-zinc-800 px-3 py-2 text-sm focus:outline-none focus:border-amber-500"
        />
        {error && <p className="text-sm text-red-400">{error}</p>}
        <button
          disabled={busy}
          className="w-full rounded-lg bg-amber-500 text-zinc-950 font-semibold py-2 text-sm hover:bg-amber-400 disabled:opacity-50"
        >
          {busy ? "…" : needsSetup ? "Create account" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
