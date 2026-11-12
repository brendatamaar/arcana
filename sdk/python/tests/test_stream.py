import base64
import json
import threading
import unittest

from websockets.exceptions import ConnectionClosed

from arcana import Arcana, ArcanaError

from . import fake as fakes


def b64(data: bytes | str) -> str:
    return base64.b64encode(data.encode() if isinstance(data, str) else data).decode()


def exit_frame(code: int = 0, cancelled: bool = False) -> str:
    return json.dumps({"type": "exit", "exit_code": code, "timed_out": False, "truncated": False, "cancelled": cancelled, "duration_ms": 12})


class StreamTests(unittest.TestCase):
    def setUp(self) -> None:
        self.fake, self.ws_url = fakes.start()
        self.addCleanup(self.fake.close)
        self.arcana = Arcana(self.ws_url, fakes.API_KEY)
        self.addCleanup(self.arcana.close)

    def test_start_frame_binary_output_order_and_real_exit(self) -> None:
        def scenario(socket) -> None:
            socket.recv()
            socket.send(json.dumps({"type": "started", "exec_id": "exe_1"}))
            socket.send(json.dumps({"type": "stdout", "data": b64(b"\x00\xff")}))
            socket.send(json.dumps({"type": "stderr", "data": b64("warn")}))
            socket.send(exit_frame(7))

        self.fake.on_socket = scenario
        with self.arcana.sandboxes.exec_stream("sbx_a", cmd=["sh", "-c", "x"], cwd="/workspace", env={"A": "1"}, timeout_seconds=9) as stream:
            self.assertEqual(stream.exec_id, "exe_1")
            events = [(e.kind, e.data) for e in stream]
            self.assertEqual(events, [("stdout", b"\x00\xff"), ("stderr", b"warn")])
            exit_ = stream.wait()
        self.assertEqual((exit_.exit_code, exit_.timed_out, exit_.cancelled, exit_.duration_ms), (7, False, False, 12))
        self.assertEqual(self.fake.frames[0], {"type": "start", "cmd": ["sh", "-c", "x"], "cwd": "/workspace", "env": {"A": "1"}, "timeout_seconds": 9})

    def test_stdin_chunking_close_resize_and_cancel(self) -> None:
        def scenario(socket) -> None:
            while True:
                try:
                    frame = json.loads(socket.recv())
                except ConnectionClosed:
                    return
                if frame["type"] == "start":
                    socket.send(json.dumps({"type": "started", "exec_id": "exe_2"}))
                if frame["type"] == "cancel":
                    socket.send(exit_frame(137, cancelled=True))

        self.fake.on_socket = scenario
        with self.arcana.sandboxes.exec_stream("sbx_a", shell="cat", tty=True, cols=80, rows=24) as stream:
            stream.write(b"\x01" * 40000)
            stream.write("tail")
            stream.resize(100, 30)
            stream.close_stdin()
            stream.close_stdin()
            with self.assertRaises(ValueError):
                stream.write("late")
            stream.cancel()
            self.assertTrue(stream.wait().cancelled)
        stdin = [f for f in self.fake.frames if f["type"] == "stdin"]
        self.assertEqual(len(stdin), 4, "40000 bytes is 3 chunks of at most 16384, plus the tail")
        self.assertTrue(all(len(base64.b64decode(f["data"])) <= 16384 for f in stdin))
        self.assertEqual(self.fake.frames[0], {"type": "start", "shell": "cat", "tty": True, "cols": 80, "rows": 24})
        self.assertEqual([f["type"] for f in self.fake.frames[-4:]], ["stdin", "resize", "stdin_close", "cancel"])

    def test_collect_decodes_text(self) -> None:
        def scenario(socket) -> None:
            socket.recv()
            socket.send(json.dumps({"type": "started", "exec_id": "exe_3"}))
            socket.send(json.dumps({"type": "stdout", "data": b64("héllo")}))
            socket.send(exit_frame(0))

        self.fake.on_socket = scenario
        with self.arcana.sandboxes.exec_stream("sbx_a", cmd=["echo"]) as stream:
            out, err, exit_ = stream.collect()
        self.assertEqual((out, err, exit_.exit_code), ("héllo", "", 0))

    def test_http_failure_before_upgrade_is_an_arcana_error(self) -> None:
        bad = Arcana(self.ws_url, "wrong")
        self.addCleanup(bad.close)
        with self.assertRaises(ArcanaError) as caught:
            bad.sandboxes.exec_stream("sbx_a", cmd=["ls"])
        self.assertEqual((caught.exception.status, caught.exception.code), (401, "unauthorized"))

    def test_dropped_connection_before_exit_fails_loudly(self) -> None:
        done = threading.Event()

        def scenario(socket) -> None:
            socket.recv()
            socket.send(json.dumps({"type": "started", "exec_id": "exe_4"}))
            socket.send(json.dumps({"type": "stdout", "data": b64("partial")}))
            socket.close()
            done.set()

        self.fake.on_socket = scenario
        with self.arcana.sandboxes.exec_stream("sbx_a", cmd=["sleep"]) as stream:
            with self.assertRaisesRegex(ConnectionError, "closed before the command exited"):
                stream.collect()
        self.assertTrue(done.wait(5))

    def test_server_error_frame_and_argument_validation(self) -> None:
        def scenario(socket) -> None:
            socket.recv()
            socket.send(json.dumps({"type": "error", "code": "sandbox_not_running", "message": "sandbox not running"}))

        self.fake.on_socket = scenario
        with self.assertRaises(ArcanaError) as caught:
            self.arcana.sandboxes.exec_stream("sbx_a", cmd=["ls"])
        self.assertEqual(caught.exception.code, "sandbox_not_running")
        with self.assertRaises(ValueError):
            self.arcana.sandboxes.exec_stream("sbx_a")


if __name__ == "__main__":
    unittest.main()
