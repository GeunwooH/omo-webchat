import { messageText, type UiMessage } from "./chatEntries";

/**
 * Shell-style prompt recall for the composer. The history is derived from the
 * transcript's user turns (oldest first) so it survives reloads and includes
 * messages sent from other clients of the same session; consecutive repeats
 * collapse into one entry, exactly like a shell's history.
 */
export function promptHistoryFrom(messages: readonly UiMessage[]): readonly string[] {
  const history: string[] = [];
  for (const message of messages) {
    if (message.role !== "user") continue;
    const text = messageText(message);
    if (text.trim() === "") continue;
    if (history[history.length - 1] === text) continue;
    history.push(text);
  }
  return history;
}

/** Active recall: `index` points into the history; `stash` is the draft that
 *  was in the composer before recall started and is restored past the newest
 *  entry. `null` means the composer is not navigating history. */
export interface PromptRecall {
  readonly index: number;
  readonly stash: string;
}

export interface RecallStep {
  readonly recall: PromptRecall | null;
  readonly input: string;
}

/** ArrowUp: start recalling only from an empty composer, then walk older
 *  while recall is active (editing the recalled text ends it, see the
 *  composer). Returns null when the key should keep its caret behavior. */
export function recallPrevious(history: readonly string[], recall: PromptRecall | null, input: string): RecallStep | null {
  if (history.length === 0) return null;
  if (recall === null) {
    if (input !== "") return null;
    const index = history.length - 1;
    return { recall: { index, stash: input }, input: history[index] ?? "" };
  }
  const index = Math.max(0, recall.index - 1);
  return { recall: { ...recall, index }, input: history[index] ?? "" };
}

/** ArrowDown: walk newer, and past the newest entry restore the stashed draft. */
export function recallNext(history: readonly string[], recall: PromptRecall | null): RecallStep | null {
  if (recall === null) return null;
  const index = recall.index + 1;
  if (index >= history.length) return { recall: null, input: recall.stash };
  return { recall: { ...recall, index }, input: history[index] ?? "" };
}
