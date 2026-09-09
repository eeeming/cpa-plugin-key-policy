import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { setSession, verifyCredentials, isInsecureBase } from "../store/session";
import { isEmbedded } from "../store/panelAuth";
import { errText } from "../api/error";
import { useT } from "../i18n";

export default function Login() {
  const nav = useNavigate();
  const t = useT();
  // Default to the current page origin: when the UI is hosted by CPA at
  // /v0/resource/plugins/cpa-key-quota/index.html, the API is on the same
  // origin, so same-origin requests avoid CORS and hit the right host:port.
  // In standalone dev (vite), origin is the dev server, which the vite proxy
  // forwards to CPA — still correct.
  const [baseUrl, setBaseUrl] = useState(
    typeof window !== "undefined" ? window.location.origin : "http://127.0.0.1:8317",
  );
  const [secretKey, setSecretKey] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");
    if (!secretKey.trim()) {
      setError(t("login.secretRequired"));
      return;
    }
    setBusy(true);
    try {
      // Verify BEFORE creating the session: setting the session first mounts the
      // authenticated UI, unmounts this form, and loses the error message.
      await verifyCredentials(fetch, baseUrl, secretKey);
      setSession(baseUrl, secretKey);
      nav("/keys");
    } catch (err) {
      setError(errText(err, t("login.loginFailed")));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-page">
      <div className="lp-brand">
        <div className="lp-title">{t("header.title")}</div>
        <div className="lp-sub">{t("login.subTitle")}</div>
      </div>
      <form className="card lp-card" onSubmit={submit}>
        <div className="form-row">
          <label htmlFor="login-base-url">{t("login.baseUrl")}</label>
          <input
            id="login-base-url"
            className="input"
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
            placeholder={t("login.baseUrlPlaceholder")}
            autoFocus
          />
          {isInsecureBase(baseUrl) && (
            <div className="lp-warn" role="alert">{t("login.insecureBase")}</div>
          )}
        </div>
        <div className="form-row">
          <label htmlFor="login-management-key">{t("login.managementKey")}</label>
          <input
            id="login-management-key"
            className="input"
            type="password"
            value={secretKey}
            onChange={(e) => setSecretKey(e.target.value)}
            placeholder={t("login.managementKeyPlaceholder")}
          />
        </div>
        {error && <div className="error" role="alert">{error}</div>}
        <button className="btn primary" type="submit" disabled={busy}>
          {busy ? t("login.verifying") : t("login.submit")}
        </button>
        <div className="lp-note">{t("login.memoryNote")}</div>
        {isEmbedded() && (
          <div className="lp-note">{t("login.embeddedFallback")}</div>
        )}
      </form>
    </div>
  );
}
