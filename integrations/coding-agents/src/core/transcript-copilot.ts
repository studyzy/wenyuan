/** Copilot CLI transcript reader: its session-state events.jsonl user/assistant records. */
import type { TransportTurn } from "./transcript";
import { readJsonl } from "./jsonl";
import { stripInjectedMemory } from "./transcript-util";

/** Normalize Copilot CLI's session-state event log into user/assistant turns. */
export function readCopilotTranscript(path: string): TransportTurn[] {
  const turns: TransportTurn[] = [];
  for (const rawLine of readJsonl(path)) {
    try {
      const event = JSON.parse(rawLine) as {
        type?: string;
        timestamp?: string;
        data?: { content?: string };
      };
      const role =
        event.type === "user.message"
          ? "user"
          : event.type === "assistant.message"
            ? "assistant"
            : undefined;
      const content =
        typeof event.data?.content === "string"
          ? stripInjectedMemory(event.data.content).trim()
          : "";
      if (!role || !content) continue;
      turns.push({ role, content, ...(event.timestamp ? { timestamp: event.timestamp } : {}) });
    } catch {
      // malformed events are ignored so the stop hook stays fail-open
    }
  }
  return turns;
}
