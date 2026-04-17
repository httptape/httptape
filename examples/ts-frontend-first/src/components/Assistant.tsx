import { useEffect, useRef, useState } from "react";
import { useAssistantStream } from "../useAssistantStream";

interface Query {
  label: string;
  path: string;
}

const QUERIES: Query[] = [
  { label: "Best headphones for office use?", path: "/api/assist/headphones" },
  { label: "Which keyboard for a developer?", path: "/api/assist/keyboard" },
  { label: "Hub for 4K monitor + USB-C laptop?", path: "/api/assist/hub" },
];

export function Assistant() {
  const [open, setOpen] = useState(false);
  const { text, status, start } = useAssistantStream();
  const [question, setQuestion] = useState<string | null>(null);
  const bodyRef = useRef<HTMLDivElement>(null);

  // Auto-scroll to bottom as text streams or new messages appear.
  useEffect(() => {
    if (bodyRef.current) {
      bodyRef.current.scrollTop = bodyRef.current.scrollHeight;
    }
  }, [text, status, question, open]);

  // Esc closes the modal.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

  const handleClick = (q: Query) => {
    setQuestion(q.label);
    start(q.path);
  };

  return (
    <>
      <button
        className="assistant-trigger"
        onClick={() => setOpen(true)}
        aria-label="Ask the assistant"
      >
        <span className="assistant-trigger-icon" aria-hidden>✨</span>
        Ask the assistant
      </button>

      {open && (
        <div
          className="assistant-backdrop"
          onClick={() => setOpen(false)}
          role="presentation"
        >
          <div
            className="assistant-modal"
            onClick={(e) => e.stopPropagation()}
            role="dialog"
            aria-modal="true"
            aria-label="Assistant"
          >
            <header className="assistant-modal-header">
              <div className="assistant-modal-title">
                <span className="chat-avatar" aria-hidden>AI</span>
                <div>
                  <h3>Shopping assistant</h3>
                  <p className="assistant-modal-subtitle">Powered by a recorded SSE fixture — same wire format as OpenAI streaming.</p>
                </div>
              </div>
              <button
                className="assistant-close"
                onClick={() => setOpen(false)}
                aria-label="Close"
              >
                ×
              </button>
            </header>

            <div className="assistant-modal-body" ref={bodyRef}>
              <div className="assistant-buttons">
                {QUERIES.map((q) => (
                  <button
                    key={q.path}
                    className={`assistant-query-btn ${question === q.label ? "is-active" : ""}`}
                    disabled={status === "streaming"}
                    onClick={() => handleClick(q)}
                  >
                    {q.label}
                  </button>
                ))}
              </div>

              {question && (
                <div className="chat">
                  <div className="chat-msg chat-msg-user">
                    <div className="chat-bubble chat-bubble-user">{question}</div>
                  </div>
                  <div className="chat-msg chat-msg-ai">
                    <div className="chat-avatar" aria-hidden>AI</div>
                    <div className="chat-bubble chat-bubble-ai">
                      {text || <span className="chat-placeholder">Thinking…</span>}
                      {status === "streaming" && <span className="chat-cursor" />}
                    </div>
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>
      )}
    </>
  );
}
