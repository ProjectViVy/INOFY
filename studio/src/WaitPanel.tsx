import { useState } from "react";
import type { JsonValue } from "./schema";
import { t } from "./i18n";

export interface WaitPrompt {
  request_id: string;
  kind?: string;
  prompt?: string;
  continuation_ref?: string;
}

interface Props {
  waits: WaitPrompt[];
  onResume(answers: Record<string, JsonValue>): Promise<void>;
  busy?: boolean;
}

// One prompt per outstanding wait. The server already redacts
// prompt text — the panel renders it verbatim and only submits when
// every wait has an answer (partial resumes are rejected server-side).
export function WaitPanel({ waits, onResume, busy }: Props) {
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const complete = waits.every((w) => (answers[w.request_id] ?? "") !== "");

  if (waits.length === 0) return null;
  return (
    <form
      className="wait-panel"
      aria-label={t("run.waiting")}
      onSubmit={(e) => {
        e.preventDefault();
        if (!complete) return;
        const parsed: Record<string, JsonValue> = {};
        for (const w of waits) {
          const raw = answers[w.request_id] ?? "";
          try {
            parsed[w.request_id] = JSON.parse(raw) as JsonValue;
          } catch {
            parsed[w.request_id] = raw;
          }
        }
        void onResume(parsed);
      }}
    >
      {waits.map((w) => (
        <label key={w.request_id}>
          <span className="prompt">{w.prompt ?? w.request_id}</span>
          <input
            value={answers[w.request_id] ?? ""}
            onChange={(e) =>
              setAnswers({ ...answers, [w.request_id]: e.target.value })
            }
          />
        </label>
      ))}
      <button type="submit" disabled={!complete || busy}>
        {t("action.resume")}
      </button>
    </form>
  );
}
