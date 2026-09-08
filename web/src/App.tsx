import { Routes, Route, Navigate, useNavigate } from "react-router-dom";
import { useEffect, useState } from "react";
import { isAuthed, subscribe, clearSession, getSession, bootstrapFromPanel } from "./store/session";
import { useT } from "./i18n";
import Login from "./pages/Login";
import Policy from "./pages/Policy";

function useAuthTick() {
  const [, setTick] = useState(0);
  useEffect(() => subscribe(() => setTick((t) => t + 1)), []);
  return isAuthed();
}

function TopNav() {
  const t = useT();
  const nav = useNavigate();
  const s = getSession();
  if (!s) return null;
  return (
    <div className="topnav">
      <div className="topnav-inner">
        <div className="topnav-brand">
          <span className="tn-title">{t("header.title")}</span>
          <span className="tn-sub">{s.baseUrl}</span>
        </div>
        <div className="topnav-actions">
          <button className="btn sm" onClick={() => { clearSession(); nav("/login"); }}>
            {t("header.logout")}
          </button>
        </div>
      </div>
    </div>
  );
}

function Shell() {
  const authed = useAuthTick();
  const [bootstrapped, setBootstrapped] = useState(false);
  const t = useT();

  useEffect(() => {
    if (authed || bootstrapped) return;
    let alive = true;
    void bootstrapFromPanel().finally(() => {
      if (alive) setBootstrapped(true);
    });
    return () => { alive = false; };
  }, [authed, bootstrapped]);

  if (!authed) {
    if (!bootstrapped) {
      return <div className="app muted" style={{ padding: "40px 20px" }}>{t("session.restoring")}</div>;
    }
    return (
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    );
  }
  return (
    <div className="app">
      <TopNav />
      <Routes>
        <Route path="/keys" element={<Policy />} />
        <Route path="*" element={<Navigate to="/keys" replace />} />
      </Routes>
    </div>
  );
}

export default function App() {
  return (
    <Routes>
      <Route path="/*" element={<Shell />} />
    </Routes>
  );
}
