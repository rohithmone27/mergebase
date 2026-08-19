import { Link, Outlet } from "react-router-dom";
import { useState } from "react";
import { api } from "./api";

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
      <div className="demo-note">
        Demo environment — data may reset periodically. Anything you create here is for exploration, not safekeeping.
      </div>
      <header className="topbar">
        <Link to="/" className="logo">
          <span className="dot" /> Mergebase
        </Link>
        <span className="crumb">version control for database schemas</span>
        <span className="spacer" />
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
