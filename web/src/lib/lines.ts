import { AnsiParser, type Segment } from "./ansi";

export type StreamName = "stdout" | "stderr";

export interface Line {
  id: number;
  stream: StreamName;
  segments: Segment[];
}

/** Bounds on retained output: the viewer never holds more than this, however much a command prints. */
export const MAX_LINES = 20_000;
export const MAX_LINE_CHARS = 4096;

/**
 * Turns decoded output chunks into display lines. It keeps one ANSI parser per stream so colour
 * state does not leak between stdout and stderr, treats a bare carriage return as "rewrite this
 * line" (progress bars), and wraps very long lines so every row stays a fixed height.
 */
export class LineBuffer {
  private lines: Line[] = [];
  private nextId = 1;
  private current: Line | null = null;
  private currentChars = 0;
  private pendingCR = false;
  private parsers: Record<StreamName, AnsiParser> = {
    stdout: new AnsiParser(),
    stderr: new AnsiParser(),
  };
  private decoders: Record<StreamName, TextDecoder> = {
    stdout: new TextDecoder("utf-8", { fatal: false }),
    stderr: new TextDecoder("utf-8", { fatal: false }),
  };
  /** Lines discarded from the front because MAX_LINES was exceeded. */
  dropped = 0;
  /** Bumped on every change, for cheap change detection. */
  version = 0;

  get length(): number {
    return this.lines.length + (this.current ? 1 : 0);
  }

  lineAt(index: number): Line | undefined {
    return index < this.lines.length
      ? this.lines[index]
      : index === this.lines.length
        ? (this.current ?? undefined)
        : undefined;
  }

  /** Appends raw bytes from one stream. Multi-byte characters split across calls are handled. */
  appendBytes(stream: StreamName, bytes: Uint8Array): void {
    this.appendText(stream, this.decoders[stream].decode(bytes, { stream: true }));
  }

  appendText(stream: StreamName, text: string): void {
    if (this.current && this.current.stream !== stream) this.endLine();
    for (const segment of this.parsers[stream].push(text)) this.addSegment(stream, segment);
    this.version++;
  }

  private startLine(stream: StreamName): Line {
    this.current = { id: this.nextId++, stream, segments: [] };
    this.currentChars = 0;
    return this.current;
  }

  private endLine(): void {
    if (!this.current) return;
    this.lines.push(this.current);
    this.current = null;
    this.currentChars = 0;
    if (this.lines.length > MAX_LINES) {
      const excess = this.lines.length - MAX_LINES;
      this.lines.splice(0, excess);
      this.dropped += excess;
    }
  }

  private put(stream: StreamName, text: string, segment: Segment): void {
    if (text === "") return;
    const line = this.current ?? this.startLine(stream);
    const last = line.segments[line.segments.length - 1];
    const style = segment.style;
    if (last && JSON.stringify(last.style) === JSON.stringify(style)) last.text += text;
    else line.segments.push({ text, style });
    this.currentChars += text.length;
  }

  private addSegment(stream: StreamName, segment: Segment): void {
    let run = "";
    const flush = () => {
      if (run !== "") this.put(stream, run, segment);
      run = "";
    };
    for (const ch of segment.text) {
      if (this.pendingCR) {
        this.pendingCR = false;
        if (ch !== "\n") {
          // A lone carriage return: the next text replaces what the line showed so far.
          flush();
          this.current = null;
          this.currentChars = 0;
        }
      }
      if (ch === "\r") {
        flush();
        this.pendingCR = true;
      } else if (ch === "\n") {
        flush();
        if (!this.current) this.startLine(stream);
        this.endLine();
      } else {
        run += ch;
        if (this.currentChars + run.length >= MAX_LINE_CHARS) {
          flush();
          this.endLine();
        }
      }
    }
    flush();
  }
}
