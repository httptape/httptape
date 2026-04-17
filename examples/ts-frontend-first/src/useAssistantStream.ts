import { useCallback, useRef, useState } from "react";

const API_URL = import.meta.env.VITE_API_URL || "http://localhost:3001";

export type AssistantStatus = "idle" | "streaming" | "done";

interface AssistantState {
  text: string;
  status: AssistantStatus;
}

/**
 * Hook that opens an EventSource on the given path and accumulates
 * streamed text deltas. Expects SSE events with JSON payloads of the
 * form `{"delta": "..."}` (OpenAI-style wire format). The stream ends
 * when a `[DONE]` sentinel event is received.
 */
export function useAssistantStream(): AssistantState & { start: (path: string) => void } {
  const [text, setText] = useState("");
  const [status, setStatus] = useState<AssistantStatus>("idle");
  const esRef = useRef<EventSource | null>(null);

  const start = useCallback((path: string) => {
    // Close any existing stream
    if (esRef.current) {
      esRef.current.close();
      esRef.current = null;
    }

    setText("");
    setStatus("streaming");

    const es = new EventSource(`${API_URL}${path}`);
    esRef.current = es;

    es.onmessage = (e: MessageEvent) => {
      const raw: string = e.data;

      // [DONE] sentinel — stream is complete
      if (raw === "[DONE]") {
        es.close();
        esRef.current = null;
        setStatus("done");
        return;
      }

      // Parse JSON delta payload
      try {
        const parsed: { delta: string } = JSON.parse(raw);
        if (typeof parsed.delta === "string") {
          setText((prev) => prev + parsed.delta);
        }
      } catch {
        // Ignore malformed events
      }
    };

    es.onerror = () => {
      es.close();
      esRef.current = null;
      setStatus("done");
    };
  }, []);

  return { text, status, start };
}
