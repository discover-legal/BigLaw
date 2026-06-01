import { useEffect, useState } from "react";
import { motion } from "framer-motion";
import { api } from "./api";

/** Shown only when auth is enabled and there's no session (production login). */
export function Login() {
  const [providers, setProviders] = useState<{ google: boolean; microsoft: boolean; linkedin: boolean } | null>(null);
  const error = new URLSearchParams(window.location.search).has("auth_error");

  useEffect(() => { api.authProviders().then(setProviders).catch(() => setProviders({ google: false, microsoft: false, linkedin: false })); }, []);

  const buttons: Array<{ key: "google" | "microsoft" | "linkedin"; label: string; glyph: string }> = [
    { key: "google", label: "Continue with Google", glyph: "G" },
    { key: "microsoft", label: "Continue with Microsoft", glyph: "⊞" },
    { key: "linkedin", label: "Continue with LinkedIn", glyph: "in" },
  ];
  const enabled = buttons.filter((b) => providers?.[b.key]);

  return (
    <div className="login-scrim">
      <motion.div className="login-card" initial={{ opacity: 0, y: 16 }} animate={{ opacity: 1, y: 0 }}>
        <div className="brand-mark login-brand">
          <span className="big">Big</span>&nbsp;<span className="michael">Michael</span><span className="dot">.</span>
        </div>
        <div className="login-sub">Legal Intelligence Bench</div>

        {error && <div className="login-error">Sign-in failed. Please try again.</div>}

        <div className="login-buttons">
          {providers === null && <div className="placeholder">Loading…</div>}
          {providers !== null && enabled.length === 0 && (
            <div className="login-none">
              No sign-in providers are configured. Set the OAuth client IDs/secrets
              in the server environment (Google / Microsoft / LinkedIn).
            </div>
          )}
          {enabled.map((b) => (
            <a key={b.key} className="login-btn" href={`/auth/${b.key}/login`}>
              <span className="login-glyph">{b.glyph}</span>{b.label}
            </a>
          ))}
        </div>
      </motion.div>
    </div>
  );
}
