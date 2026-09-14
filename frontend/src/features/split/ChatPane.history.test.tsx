import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ChatServerFrame } from "../../lib/chatWs";
import { pressKey, renderChatPane, requireElement, setTextareaValue } from "./chatPaneTestHarness";

const transcript: ChatServerFrame = {
	type: "entries",
	sessionId: "chat-1",
	final: true,
	entries: [
		{ type: "message", id: "u1", message: { role: "user", content: "first question" } },
		{ type: "message", id: "a1", message: { role: "assistant", content: "first answer" } },
		{ type: "message", id: "u2", message: { role: "user", content: "second\nline two" } },
		{ type: "message", id: "a2", message: { role: "assistant", content: "second answer" } },
	],
};

describe("ChatPane prompt history recall", () => {
	let container: HTMLDivElement;
	let root: Root;

	beforeEach(() => {
		vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
		vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => { callback(0); return 0; });
		Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
		container = document.createElement("div");
		document.body.appendChild(container);
		root = createRoot(container);
	});

	afterEach(async () => {
		await act(async () => {
			root.unmount();
		});
		container.remove();
		vi.unstubAllGlobals();
		Reflect.deleteProperty(HTMLElement.prototype, "scrollIntoView");
	});

	function mount() {
		const rendered = renderChatPane(root);
		act(() => rendered.deliver(transcript));
		const input = requireElement(container.querySelector<HTMLTextAreaElement>("textarea"), "missing chat input");
		return { ...rendered, input };
	}

	/** Dispatch a keydown inside act and report whether the composer claimed it. */
	function press(input: HTMLTextAreaElement, key: string, options?: { readonly isComposing?: boolean }): boolean {
		let event: KeyboardEvent | undefined;
		act(() => { event = pressKey(input, key, options ?? {}); });
		return event?.defaultPrevented ?? false;
	}

	it("recalls sent prompts newest-first with ArrowUp from an empty composer and puts the caret at the end", () => {
		const { input } = mount();
		expect(press(input, "ArrowUp")).toBe(true);
		expect(input.value).toBe("second\nline two");
		expect(input.selectionStart).toBe(input.value.length);

		// While browsing, the arrow keeps stepping even though the caret sits on
		// the last line of a multi-line prompt.
		expect(press(input, "ArrowUp")).toBe(true);
		expect(input.value).toBe("first question");
		expect(press(input, "ArrowUp")).toBe(true);
		expect(input.value).toBe("first question");
	});

	it("walks back down with ArrowDown and restores the draft past the newest prompt", () => {
		const { input } = mount();
		press(input, "ArrowUp");
		press(input, "ArrowUp");
		expect(input.value).toBe("first question");
		expect(press(input, "ArrowDown")).toBe(true);
		expect(input.value).toBe("second\nline two");
		expect(press(input, "ArrowDown")).toBe(true);
		expect(input.value).toBe("");
		// Nothing left to walk: the key falls through to the caret.
		expect(press(input, "ArrowDown")).toBe(false);
	});

	it("never replaces an unsent draft and leaves caret movement inside it alone", () => {
		const { input } = mount();
		act(() => setTextareaValue(input, "half\nwritten"));
		expect(press(input, "ArrowUp")).toBe(false);
		expect(input.value).toBe("half\nwritten");
		act(() => input.setSelectionRange(0, 0));
		expect(press(input, "ArrowDown")).toBe(false);
		expect(input.value).toBe("half\nwritten");
	});

	it("ends recall as soon as the recalled text is edited", () => {
		const { input } = mount();
		press(input, "ArrowUp");
		act(() => setTextareaValue(input, "second\nline two edited"));
		expect(press(input, "ArrowUp")).toBe(false);
		expect(input.value).toBe("second\nline two edited");
	});

	it("keeps the command palette in charge of ArrowUp and hides it after recalling a slash command", () => {
		const { input, deliver } = mount();
		act(() => deliver({
			type: "entries", sessionId: "chat-1", final: true,
			entries: [{ type: "message", id: "u9", message: { role: "user", content: "/compact" } }],
		}));
		act(() => deliver({ type: "commands", sessionId: "chat-1", commands: [{ name: "hooks", description: "Inspect hooks", source: "extension", syntax: "slash" }] }));

		act(() => setTextareaValue(input, "/"));
		expect(container.querySelector('[role="listbox"]')).not.toBeNull();
		press(input, "ArrowUp");
		expect(input.value).toBe("/");

		act(() => setTextareaValue(input, ""));
		press(input, "ArrowUp");
		expect(input.value).toBe("/compact");
		expect(container.querySelector('[role="listbox"]')).toBeNull();
	});

	it("ignores arrows during IME composition", () => {
		const { input } = mount();
		expect(press(input, "ArrowUp", { isComposing: true })).toBe(false);
		expect(input.value).toBe("");
	});

	it("includes a prompt sent from this composer once the server echoes it", () => {
		const { input, sent, deliver } = mount();
		act(() => setTextareaValue(input, "brand new"));
		press(input, "Enter");
		expect(sent.some((frame) => frame.type === "chat.send")).toBe(true);
		expect(input.value).toBe("");
		act(() => {
			deliver({ type: "message", sessionId: "chat-1", message: { role: "user", blocks: [{ kind: "text", text: "brand new" }], ts: 1 } });
			deliver({ type: "run.done", sessionId: "chat-1", reason: "stop" });
		});
		press(input, "ArrowUp");
		expect(input.value).toBe("brand new");
	});
});
