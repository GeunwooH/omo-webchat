import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { I18nContext, translate, type I18nValue, type Lang } from "../../i18n";
import { LoginPage } from "./LoginPage";

vi.mock("./auth", () => ({ login: vi.fn() }));

describe("LoginPage", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
  });

  function render(lang: Lang, setLang: (next: Lang) => void = () => undefined): void {
    const value: I18nValue = {
      lang,
      setLang,
      t: (key, vars) => translate(lang, key, vars),
      font: "system",
      setFont: () => undefined,
      fontSize: 13,
      setFontSize: () => undefined,
    };
    act(() => {
      root.render(
        <I18nContext.Provider value={value}>
          <LoginPage onLogin={() => undefined} />
        </I18nContext.Provider>,
      );
    });
  }

  it("labels the language toggle with the language it switches to", () => {
    const setLang = vi.fn();
    render("en", setLang);
    const toggle = container.querySelector<HTMLButtonElement>(".th-login-lang");
    expect(toggle?.textContent).toBe("한국어");
    expect(toggle?.lang).toBe("ko");
    act(() => toggle?.click());
    expect(setLang).toHaveBeenCalledWith("ko");

    render("ko", setLang);
    expect(container.querySelector(".th-login-lang")?.textContent).toBe("English");
  });

  it("pairs the password with a username field for password managers", () => {
    render("en");
    const username = container.querySelector<HTMLInputElement>('form input[autocomplete="username"]');
    expect(username?.form).toBe(container.querySelector<HTMLInputElement>("#th-password")?.form);
  });

  it("renders the wordmark once, as the heading", () => {
    render("en");
    const wordmark = translate("en", "login.title");
    const standalone = Array.from(container.querySelectorAll(".th-login-card *"))
      .filter((el) => el.children.length === 0 && el.textContent?.trim() === wordmark);
    expect(standalone).toHaveLength(0);
    expect(container.querySelectorAll("h1")).toHaveLength(1);
    expect(container.querySelector("h1")?.textContent).toBe(wordmark);
  });
});
