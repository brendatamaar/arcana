import { describe, expect, it } from "vitest";
import { LineBuffer, MAX_LINES, MAX_LINE_CHARS } from "./lines";

const bytes = (s: string) => new TextEncoder().encode(s);
const rows = (b: LineBuffer) =>
  Array.from(
    { length: b.length },
    (_, i) =>
      b
        .lineAt(i)
        ?.segments.map((s) => s.text)
        .join("") ?? "",
  );

describe("LineBuffer", () => {
  it("splits on newlines and keeps the unfinished line visible", () => {
    const b = new LineBuffer();
    b.appendText("stdout", "one\ntwo\nthr");
    expect(rows(b)).toEqual(["one", "two", "thr"]);
    b.appendText("stdout", "ee\n");
    expect(rows(b)).toEqual(["one", "two", "three"]);
  });

  it("treats CRLF as one newline, including when the pair is split across chunks", () => {
    const b = new LineBuffer();
    b.appendText("stdout", "a\r");
    b.appendText("stdout", "\nb\r\nc");
    expect(rows(b)).toEqual(["a", "b", "c"]);
  });

  it("lets a bare carriage return rewrite the line, as progress bars do", () => {
    const b = new LineBuffer();
    b.appendText("stdout", "10%\r50%\r100%\ndone\n");
    expect(rows(b)).toEqual(["100%", "done"]);
  });

  it("decodes UTF-8 sequences split across byte chunks", () => {
    const b = new LineBuffer();
    const euro = bytes("€");
    b.appendBytes("stdout", euro.slice(0, 1));
    b.appendBytes("stdout", euro.slice(1));
    b.appendBytes("stdout", bytes("\n"));
    expect(rows(b)).toEqual(["€"]);
  });

  it("keeps stdout and stderr on separate lines with separate colour state", () => {
    const b = new LineBuffer();
    b.appendText("stdout", "\u001b[31mred ");
    b.appendText("stderr", "err\n");
    b.appendText("stdout", "still\n");
    expect(rows(b)).toEqual(["red ", "err", "still"]);
    expect(b.lineAt(0)?.stream).toBe("stdout");
    expect(b.lineAt(1)?.stream).toBe("stderr");
    expect(b.lineAt(1)?.segments[0]?.style).toEqual({});
    expect(b.lineAt(2)?.segments[0]?.style).toEqual({ fg: 1 });
  });

  it("wraps very long lines so rows stay a bounded size", () => {
    const b = new LineBuffer();
    b.appendText("stdout", "x".repeat(MAX_LINE_CHARS * 2 + 10) + "\n");
    expect(b.length).toBeGreaterThanOrEqual(3);
    for (const row of rows(b)) expect(row.length).toBeLessThanOrEqual(MAX_LINE_CHARS);
    expect(rows(b).join("").length).toBe(MAX_LINE_CHARS * 2 + 10);
  });

  it("drops the oldest lines beyond its cap and says how many", () => {
    const b = new LineBuffer();
    for (let i = 0; i < MAX_LINES + 50; i++) b.appendText("stdout", `line ${i}\n`);
    expect(b.length).toBeLessThanOrEqual(MAX_LINES + 1);
    expect(b.dropped).toBe(50);
    expect(rows(b)[0]).toBe("line 50");
  });

  it("changes version on every append so views can re-render", () => {
    const b = new LineBuffer();
    const before = b.version;
    b.appendText("stdout", "x");
    expect(b.version).toBeGreaterThan(before);
  });
});
