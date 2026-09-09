import { Routes, Route, Navigate, useNavigate } from "react-router-dom";
import { useEffect, useRef, useState } from "react";
import {
  isAuthed,
  subscribe,
  clearSession,
  getSession,
  bootstrapFromPanel,
  disablePanelBootstrap,
} from "./store/session";
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
          <button
            className="btn sm"
            onClick={() => {
              // Logging out must stay logged out: block the panel-key bootstrap
              // from restoring the session on the next render.
              disablePanelBootstrap();
              clearSession();
              nav("/login");
            }}
          >
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
  const bootstrapStarted = useRef(false);
  const t = useT();

  useEffect(() => {
    // Run the panel-key bootstrap exactly once per page load. Keying this on
    // `bootstrapped` alone re-ran it after every logout and logged the operator
    // straight back in when the panel had a remembered key.
    if (authed || bootstrapStarted.current) return;
    bootstrapStarted.current = true;
    void bootstrapFromPanel().finally(() => setBootstrapped(true));
  }, [authed]);

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
