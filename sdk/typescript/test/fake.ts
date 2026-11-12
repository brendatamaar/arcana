import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { WebSocketServer, type WebSocket } from "ws";

export interface Recorded {
  method: string;
  url: string;
  headers: IncomingMessage["headers"];
  body: Buffer;
}

export type Handler = (request: Recorded, response: ServerResponse) => void;

export interface Fake {
  url: string;
  requests: Recorded[];
  sockets: WebSocket[];
  frames: Array<Record<string, unknown>>;
  handler: Handler;
  onSocket: (socket: WebSocket) => void;
  close: () => Promise<void>;
}

export const json = (response: ServerResponse, status: number, value: unknown, headers: Record<string, string> = {}): void => {
  response.writeHead(status, { "Content-Type": "application/json", ...headers });
  response.end(JSON.stringify(value));
};

/** An in-process HTTP + WebSocket server standing in for Arcana. */
export async function fakeArcana(): Promise<Fake> {
  const fake: Fake = {
    url: "",
    requests: [],
    sockets: [],
    frames: [],
    handler: (_request, response) => json(response, 404, { error: { code: "internal", message: "unhandled", request_id: "req_x" } }),
    onSocket: () => undefined,
    close: async () => undefined,
  };
  const server: Server = createServer((request, response) => {
    const chunks: Buffer[] = [];
    request.on("data", (chunk: Buffer) => chunks.push(chunk));
    request.on("end", () => {
      const recorded: Recorded = { method: request.method ?? "", url: request.url ?? "", headers: request.headers, body: Buffer.concat(chunks) };
      fake.requests.push(recorded);
      fake.handler(recorded, response);
    });
  });
  const sockets = new WebSocketServer({ noServer: true });
  server.on("upgrade", (request, socket, head) => {
    if (request.headers.authorization !== "Bearer test-key") {
      socket.write('HTTP/1.1 401 Unauthorized\r\nContent-Type: application/json\r\nConnection: close\r\n\r\n{"error":{"code":"unauthorized","message":"authentication required","request_id":"req_ws"}}');
      socket.destroy();
      return;
    }
    sockets.handleUpgrade(request, socket, head, (ws) => {
      fake.sockets.push(ws);
      ws.on("message", (data) => fake.frames.push(JSON.parse(data.toString()) as Record<string, unknown>));
      fake.onSocket(ws);
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  fake.url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  fake.close = async () => {
    for (const socket of fake.sockets) socket.terminate();
    sockets.close();
    await new Promise<void>((resolve) => server.close(() => resolve()));
  };
  return fake;
}
