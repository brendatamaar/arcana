// A small ANSI/VT parser that turns terminal output into styled text segments.
//
// Output from a sandbox is hostile. The parser therefore produces only plain strings plus a
// whitelisted style, never markup: callers render segments as React text children, so HTML in the
// output stays inert, and every escape sequence is either mapped to a known style or dropped.

/** 0-7 are the normal colours and 8-15 the bright ones. */
export type ColorIndex = 0 | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | 10 | 11 | 12 | 13 | 14 | 15;

export interface Style {
  fg?: ColorIndex;
  bg?: ColorIndex;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
  inverse?: boolean;
}

export interface Segment {
  text: string;
  style: Style;
}

/** Longest escape sequence kept while waiting for its terminator; longer ones are dropped. */
const MAX_SEQUENCE = 4096;

type State = "text" | "esc" | "csi" | "osc" | "string";

const ESC = "\u001b";

function colour(n: number): ColorIndex {
  return n as ColorIndex;
}

/** Applies SGR parameters (the numbers of `ESC [ ... m`) to a style. */
export function applySgr(style: Style, params: number[]): Style {
  let next: Style = { ...style };
  const codes = params.length === 0 ? [0] : params;
  for (let i = 0; i < codes.length; i++) {
    const code = codes[i] ?? 0;
    if (code === 0) next = {};
    else if (code === 1) next.bold = true;
    else if (code === 2) next.dim = true;
    else if (code === 3) next.italic = true;
    else if (code === 4) next.underline = true;
    else if (code === 7) next.inverse = true;
    else if (code === 22) {
      delete next.bold;
      delete next.dim;
    } else if (code === 23) delete next.italic;
    else if (code === 24) delete next.underline;
    else if (code === 27) delete next.inverse;
    else if (code >= 30 && code <= 37) next.fg = colour(code - 30);
    else if (code === 39) delete next.fg;
    else if (code >= 40 && code <= 47) next.bg = colour(code - 40);
    else if (code === 49) delete next.bg;
    else if (code >= 90 && code <= 97) next.fg = colour(code - 90 + 8);
    else if (code >= 100 && code <= 107) next.bg = colour(code - 100 + 8);
    else if (code === 38 || code === 48) {
      // Extended colours: 5;n (256-colour) or 2;r;g;b. Only the 16 base colours are kept; the rest
      // is consumed so its numbers are not mistaken for other codes.
      const mode = codes[i + 1];
      if (mode === 5) {
        const n = codes[i + 2];
        if (n !== undefined && n >= 0 && n < 16) {
          if (code === 38) next.fg = colour(n);
          else next.bg = colour(n);
        }
        i += 2;
      } else if (mode === 2) {
        i += 4;
      }
    }
  }
  return next;
}

function sameStyle(a: Style, b: Style): boolean {
  return (
    a.fg === b.fg &&
    a.bg === b.bg &&
    !!a.bold === !!b.bold &&
    !!a.dim === !!b.dim &&
    !!a.italic === !!b.italic &&
    !!a.underline === !!b.underline &&
    !!a.inverse === !!b.inverse
  );
}

/**
 * Incremental parser: feed chunks as they arrive. State (the current style and any unfinished
 * escape sequence) carries across chunks, so sequences split between network frames still parse.
 */
export class AnsiParser {
  private state: State = "text";
  private sequence = "";
  private skipped = 0;
  private style: Style = {};

  /** Returns the printable text of this chunk as styled segments. Control characters other than
   * newline, tab and carriage return are removed; carriage returns are passed through for the line layer. */
  push(chunk: string): Segment[] {
    const out: Segment[] = [];
    let text = "";
    const emit = () => {
      if (text === "") return;
      const last = out[out.length - 1];
      if (last && sameStyle(last.style, this.style)) last.text += text;
      else out.push({ text, style: { ...this.style } });
      text = "";
    };
    for (const ch of chunk) {
      switch (this.state) {
        case "text":
          if (ch === ESC) {
            emit();
            this.state = "esc";
            this.sequence = "";
            this.skipped = 0;
          } else if (ch === "\n" || ch === "\t" || ch === "\r") {
            text += ch;
          } else {
            const code = ch.codePointAt(0) ?? 0;
            // Drop other C0 controls, DEL and C1 controls: they can move the cursor or ring bells.
            if (code >= 0x20 && code !== 0x7f && !(code >= 0x80 && code <= 0x9f)) text += ch;
          }
          break;
        case "esc":
          if (ch === "[") this.state = "csi";
          else if (ch === "]") this.state = "osc";
          else if (ch === "P" || ch === "^" || ch === "_" || ch === "X") this.state = "string";
          else this.state = "text"; // two-character escape (ESC c, ESC 7, ...): dropped
          break;
        case "csi": {
          const code = ch.codePointAt(0) ?? 0;
          if (code >= 0x40 && code <= 0x7e) {
            if (ch === "m" && /^[0-9;:]*$/.test(this.sequence)) {
              const params =
                this.sequence === ""
                  ? []
                  : this.sequence.split(/[;:]/).map((p) => (p === "" ? 0 : Number(p)));
              this.style = applySgr(this.style, params);
            }
            this.state = "text";
          } else if (code >= 0x20 && code <= 0x3f) {
            this.sequence += ch;
            if (this.sequence.length > MAX_SEQUENCE) this.state = "text";
          } else {
            this.state = "text"; // malformed: abandon the sequence
          }
          break;
        }
        case "osc":
        case "string":
          // Window titles, hyperlinks and device strings end with BEL or ST (ESC \). They are dropped whole.
          if (ch === "\u0007") this.state = "text";
          else if (ch === ESC)
            this.state = "esc"; // the next char (\) completes ST and is dropped
          else if (++this.skipped > MAX_SEQUENCE) this.state = "text";
          break;
      }
    }
    emit();
    return out;
  }
}
