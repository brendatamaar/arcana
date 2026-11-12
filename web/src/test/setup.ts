import { afterAll, afterEach, beforeAll } from "vitest";
import { cleanup } from "@testing-library/react";
import { server } from "./server";

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});
afterEach(() => {
  cleanup();
  server.resetHandlers();
});
afterAll(() => {
  server.close();
});

// jsdom has no layout. Give scroll containers a size so the virtualised log renders rows.
Object.defineProperty(HTMLElement.prototype, "getBoundingClientRect", {
  configurable: true,
  value() {
    return {
      x: 0,
      y: 0,
      top: 0,
      left: 0,
      right: 800,
      bottom: 400,
      width: 800,
      height: 400,
      toJSON: () => ({}),
    };
  },
});
// The virtualiser measures its scroll container with clientWidth/clientHeight.
Object.defineProperty(Element.prototype, "clientHeight", { configurable: true, get: () => 400 });
Object.defineProperty(Element.prototype, "clientWidth", { configurable: true, get: () => 800 });
Element.prototype.scrollTo = () => undefined;
