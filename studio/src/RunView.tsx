import { useEffect, useRef, useState } from "react";
import type { StudioTransport } from "./transport";
import type { RunEvent, RunView as RunViewT } from "./schema";
import { WaitPanel, type WaitPrompt } from "./WaitPanel";
import { t } from "./i18n";

interface Props {
  transport: StudioTransport;
  runId: string;
}

// Paints committed state only: status/generation come from getRun,
// the timeline from the committed events feed. SSE reconnects from
// the last delivered seq — a dropped stream never invents events.
export function RunView({ transport, runId }: Props) {
  const [run, setRun] = useState<RunViewT | null>(null);
  const [waits, setWaits] = useState<WaitPrompt[]>([]);
  const [events, setEvents] = useState<RunEvent[]>([]);
  const cursor = useRef(0);
  const [streamErr, setStreamErr] = useState(false);

  const refresh = async () => {
    const r = (await transport.getRun(runId)) as RunViewT & {
      waits?: WaitPrompt[];
    };
    setRun(r);
    setWaits(r.waits ?? []);
  };

  useEffect(() => {
    void refresh();
    // Polling page first (authoritative), then SSE for the tail.
    void transport.events(runId).then((p) => {
      setEvents(p.events);
      if (p.events.length)
        cursor.current = p.events[p.events.length - 1]!.seq;
    });
    const sub = transport.subscribeEvents(
      runId,
      undefined,
      (e) => {
        if (e.seq > cursor.current) {
          cursor.current = e.seq;
          setEvents((prev) =>
            prev.some((x) => x.seq === e.seq) ? prev : [...prev, e],
          );
        }
        if (e.kind === "run_waiting" || e.kind === "run_resumed") void refresh();
      },
      () => setStreamErr(true),
    );
    return () => sub.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [runId]);

  // Fallback: stream failed → slow poll from the durable cursor.
  useEffect(() => {
    if (!streamErr) return;
    const id = setInterval(async () => {
      const p = await transport.events(runId, cursor.current);
      if (p.events.length) {
        setEvents((prev) => [...prev, ...p.events]);
        cursor.current = p.events[p.events.length - 1]!.seq;
      }
      void refresh();
    }, 2000);
    return () => clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [streamErr, runId]);

  return (
    <section className="run-view" aria-label="run">
      <header>
        <strong>{runId}</strong>
        <span data-testid="run-status">{run?.status ?? "…"}</span>
        <button onClick={() => void transport.cancelRun(runId)}>
          {t("action.cancel")}
        </button>
      </header>
      <WaitPanel waits={waits} onResume={(a) => transport.resumeRun(runId, a).then(() => refresh())} />
      <ol data-testid="run-events">
        {events.map((e) => (
          <li key={e.seq}>
            <code>{e.seq}</code> {e.kind}
            {e.path ? ` ${e.path}` : ""}
          </li>
        ))}
      </ol>
    </section>
  );
}
