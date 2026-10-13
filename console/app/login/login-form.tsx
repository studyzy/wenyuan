"use client";

import { useState, type FormEvent } from "react";
import { signIn } from "../../lib/client/state";
import { apiUrl } from "../../lib/client/api";

type Props = {
  returnTo: string;
  locale: string;
  languages: string[];
  labels: { accessKey: string; signIn: string; language: string };
};

/** Login form with a language switcher. */
export function LoginForm({ returnTo, locale, languages, labels }: Props) {
  const [accessKey, setAccessKey] = useState("");
  const [error, setError] = useState<string | null>(null);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError(null);
    try {
      await signIn(accessKey, returnTo);
      window.location.assign(apiUrl(returnTo));
    } catch {
      setError("api.errors.auth.wrongKey");
    }
  }

  return (
    <form onSubmit={onSubmit}>
      <label>
        {labels.accessKey}
        <input
          type="password"
          value={accessKey}
          onChange={(event) => setAccessKey(event.target.value)}
          autoComplete="off"
        />
      </label>
      <label>
        {labels.language}
        <select defaultValue={locale} onChange={(event) => {
          document.cookie = `NEXT_LOCALE=${event.target.value}; Path=/`;
        }}>
          {languages.map((code) => (
            <option key={code} value={code}>
              {code}
            </option>
          ))}
        </select>
      </label>
      <button type="submit">{labels.signIn}</button>
      {error ? <p role="alert">{error}</p> : null}
    </form>
  );
}