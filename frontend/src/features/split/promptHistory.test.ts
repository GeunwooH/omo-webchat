import { describe, expect, it } from "vitest";
import type { UiMessage } from "./chatEntries";
import { promptHistoryFrom, recallNext, recallPrevious } from "./promptHistory";

const user = (text: string): UiMessage => ({ role: "user", blocks: [{ kind: "text", text }], timestamp: 0 });
const assistant = (text: string): UiMessage => ({ role: "assistant", blocks: [{ kind: "text", text }], timestamp: 0 });

describe("promptHistoryFrom", () => {
  it("keeps only non-empty user turns, oldest first, collapsing consecutive repeats", () => {
    const messages: UiMessage[] = [
      user("first"), assistant("reply"), user("   "), user("second"), user("second"), user("first"),
    ];
    expect(promptHistoryFrom(messages)).toEqual(["first", "second", "first"]);
  });

  it("joins multi-block text and preserves newlines", () => {
    const message: UiMessage = { role: "user", blocks: [{ kind: "text", text: "line one\n" }, { kind: "text", text: "line two" }], timestamp: 0 };
    expect(promptHistoryFrom([message])).toEqual(["line one\nline two"]);
  });
});

describe("recallPrevious / recallNext", () => {
  const history = ["a", "b", "c"];

  it("starts from the newest entry only when the composer is empty", () => {
    expect(recallPrevious(history, null, "draft")).toBeNull();
    expect(recallPrevious([], null, "")).toBeNull();
    expect(recallPrevious(history, null, "")).toEqual({ recall: { index: 2, stash: "" }, input: "c" });
  });

  it("walks older and clamps at the oldest entry", () => {
    const step1 = recallPrevious(history, { index: 2, stash: "" }, "c");
    expect(step1).toEqual({ recall: { index: 1, stash: "" }, input: "b" });
    const step2 = recallPrevious(history, { index: 0, stash: "" }, "a");
    expect(step2).toEqual({ recall: { index: 0, stash: "" }, input: "a" });
  });

  it("walks newer and restores the stashed draft past the newest entry", () => {
    expect(recallNext(history, null)).toBeNull();
    expect(recallNext(history, { index: 0, stash: "typed" })).toEqual({ recall: { index: 1, stash: "typed" }, input: "b" });
    expect(recallNext(history, { index: 2, stash: "typed" })).toEqual({ recall: null, input: "typed" });
  });
});
