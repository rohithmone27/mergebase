import { Link, Outlet } from "react-router-dom";
import { useState } from "react";
import { api } from "./api";

export function BranchGlyph({ size = 16 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor"
      strokeWidth="1.7" strokeLinecap="round" aria-hidden>
      <circle cx="4.2" cy="3.6" r="1.8" />
      <circle cx="4.2" cy="12.4" r="1.8" />
      <circle cx="11.8" cy="3.6" r="1.8" />
      <path d="M4.2 5.4v5.2M11.8 5.4c0 3-3 3.4-5.6 3.6" />
    </svg>
  );
}

export function TableGlyph() {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden>
      <rect x="1.5" y="2" width="11" height="10" rx="1.5" />
      <path d="M1.5 5.5h11M5.5 5.5V12" />
    </svg>
  );
}

export function Layout() {
  const [resetting, setResetting] = useState(false);

  async function resetDemo() {
    if (!window.confirm("Reset the demo workspace? All projects and branches revert to the seeded state.")) return;
    setResetting(true);
    try {
      await api.demoReset();
      window.location.href = "/";
    } finally {
      setResetting(false);
    }
  }

  return (
    <>
      <header className="topbar">
        <Link to="/" className="logo">
          <span style={{ color: "var(--accent)" }}><BranchGlyph size={18} /></span>
          mergebase
        </Link>
        <span className="tagline">version control for database schemas</span>
        <span className="spacer" />
        <span className="demo-pill" title="Anything you create here is for exploration, not safekeeping.">
          demo · data may reset
        </span>
        <button className="btn quiet" onClick={resetDemo} disabled={resetting}>
          {resetting ? "Resetting…" : "Reset demo"}
        </button>
      </header>
      <main className="page">
        <Outlet />
      </main>
    </>
  );
}
