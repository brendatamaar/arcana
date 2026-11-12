import { describe, expect, it } from "vitest";
import { AnsiParser, applySgr } from "./ansi";

const parse = (...chunks: string[]) => {
  const parser = new AnsiParser();
  return chunks.flatMap((chunk) => parser.push(chunk));
};
const text = (segments: { text: string }[]) => segments.map((s) => s.text).join("");

describe("AnsiParser", () => {
  it("passes plain text through unstyled", () => {
    expect(parse("hello\nworld")).toEqual([{ text: "hello\nworld", style: {} }]);
  });

  it("maps SGR colours, brights, attributes and resets", () => {
    const out = parse(
      "\u001b[31mred\u001b[0m \u001b[1;92mbold bright green\u001b[22;39m \u001b[44mbg\u001b[49m \u001b[4;7mux\u001b[0m",
    );
    expect(out.map((s) => [s.text, s.style])).toEqual([
      ["red", { fg: 1 }],
      [" ", {}],
      ["bold bright green", { bold: true, fg: 10 }],
      [" ", {}],
      ["bg", { bg: 4 }],
      [" ", {}],
      ["ux", { underline: true, inverse: true }],
    ]);
  });

  it("keeps only the 16 base colours of 256-colour codes and swallows truecolour parameters", () => {
    expect(parse("\u001b[38;5;9mA\u001b[0m")[0]?.style).toEqual({ fg: 9 });
    expect(parse("\u001b[38;5;200mB")[0]?.style).toEqual({});
    // 38;2;r;g;b must not leak its numbers as codes (1 = bold, 31 = red, ...).
    expect(parse("\u001b[38;2;1;31;7mC")[0]?.style).toEqual({});
    expect(parse("\u001b[48;5;4mD")[0]?.style).toEqual({ bg: 4 });
  });

  it("parses a sequence split across chunks and keeps style across chunks", () => {
    expect(parse("a\u001b[3", "1mb", "c")).toEqual([
      { text: "a", style: {} },
      { text: "b", style: { fg: 1 } },
      { text: "c", style: { fg: 1 } }, // one segment per chunk; LineBuffer merges neighbours
    ]);
  });

  it("drops every non-SGR escape: cursor movement, erase, titles, hyperlinks, device strings", () => {
    const hostile =
      "A\u001b[2J\u001b[H\u001b[10;20HB\u001b]0;evil title\u0007C\u001b]8;;http://evil.example\u001b\\link\u001b]8;;\u001b\\D\u001bPdevice\u001b\\E\u001bcF\u001b[?25lG";
    expect(text(parse(hostile))).toBe("ABClinkDEFG");
  });

  it("removes control characters but keeps newline, tab and carriage return", () => {
    expect(text(parse("a\u0007b\u0008c\u007fd\u0080e\tf\r\ng\u0000h"))).toBe("abcde\tf\r\ngh");
  });

  it("leaves HTML in the output as ordinary text", () => {
    const html = '<script>alert(1)</script><img src=x onerror="y">';
    expect(text(parse(`\u001b[31m${html}\u001b[0m`))).toBe(html);
  });

  it("abandons endless or malformed sequences instead of buffering without bound", () => {
    // An unterminated title string stops being swallowed after 4096 characters; what follows shows as text.
    const afterOsc = text(parse("\u001b]0;" + "x".repeat(10_000) + "visible"));
    expect(afterOsc.endsWith("visible")).toBe(true);
    expect(afterOsc.length).toBeGreaterThan(5_000);
    expect(afterOsc.length).toBeLessThan(10_000);
    // The same for a CSI whose parameters never end.
    const afterCsi = text(parse("\u001b[" + "9".repeat(10_000) + "Zafter"));
    expect(afterCsi.endsWith("Zafter")).toBe(true);
    expect(afterCsi.length).toBeLessThan(10_000);
    // A control character inside a CSI abandons it at once.
    expect(text(parse("\u001b[\u0001ok"))).toBe("ok");
  });

  it("never emits an escape character, whatever it is fed", () => {
    const alphabet = [
      "\u001b",
      "[",
      "]",
      "P",
      "\\",
      "\u0007",
      "m",
      ";",
      "3",
      "1",
      "8",
      "x",
      "\n",
      "\r",
      " ",
      "é",
      "<",
      ">",
    ];
    let seed = 12345;
    const next = () => (seed = (seed * 1103515245 + 12345) & 0x7fffffff);
    for (let round = 0; round < 300; round++) {
      const parser = new AnsiParser();
      let input = "";
      for (let i = 0; i < 80; i++) input += alphabet[next() % alphabet.length] ?? "";
      const cut = next() % input.length;
      const output = [...parser.push(input.slice(0, cut)), ...parser.push(input.slice(cut))];
      for (const segment of output) {
        expect(segment.text.includes("\u001b")).toBe(false);
        expect(segment.text.includes("\u0007")).toBe(false);
      }
    }
  });
});

describe("applySgr", () => {
  it("treats an empty parameter list as a reset", () => {
    expect(applySgr({ fg: 1, bold: true }, [])).toEqual({});
  });
});
