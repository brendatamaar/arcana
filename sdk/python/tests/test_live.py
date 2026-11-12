"""Runs against a real Arcana (`make sdk-test` starts one); skipped with a message otherwise."""

import os
import unittest

from arcana import Arcana, ArcanaError

LIVE = os.environ.get("ARCANA_LIVE") == "1" and os.environ.get("ARCANA_URL") and os.environ.get("ARCANA_API_KEY")


@unittest.skipUnless(LIVE, "set ARCANA_LIVE=1, ARCANA_URL and ARCANA_API_KEY to run against a live Arcana")
class LiveTests(unittest.TestCase):
    def test_lifecycle_exec_files_stream_snapshot_and_fork(self) -> None:
        template = os.environ.get("ARCANA_TEMPLATE", "base")
        arcana = Arcana(os.environ["ARCANA_URL"], os.environ["ARCANA_API_KEY"])
        sandboxes: list[str] = []
        snapshots: list[str] = []

        def cleanup() -> None:
            for sandbox_id in sandboxes:
                try:
                    arcana.sandboxes.destroy(sandbox_id)
                except ArcanaError:
                    pass
            for snapshot_id in snapshots:
                try:
                    arcana.snapshots.delete(snapshot_id)
                except ArcanaError:
                    pass
            arcana.close()

        self.addCleanup(cleanup)
        self.assertEqual(arcana.health().status.value, "ok")
        self.assertTrue(any(t.name == template for t in arcana.templates()))
        created = arcana.sandboxes.create(template, labels={"sdk": "python"})
        sandboxes.append(created.id)
        self.assertEqual(created.state.value, "running")

        ran = arcana.sandboxes.exec(created.id, shell="echo out; echo err >&2; exit 3")
        self.assertEqual((ran.exit_code, ran.stdout.strip()), (3, "out"))

        payload = bytes([0, 255, 1, 128, 10])
        arcana.files.write(created.id, "/workspace/sub/data.bin", payload)
        self.assertEqual(arcana.files.read(created.id, "/workspace/sub/data.bin"), payload)
        self.assertTrue(any(e.path == "/workspace/sub/data.bin" for e in arcana.files.list(created.id, "/workspace", depth=2)))

        with arcana.sandboxes.exec_stream(created.id, cmd=["/bin/cat"]) as stream:
            stream.write("hello ")
            stream.write(b"world")
            stream.close_stdin()
            out, _err, exit_ = stream.collect()
        self.assertEqual((out, exit_.exit_code), ("hello world", 0))

        snapshot = arcana.snapshots.create(created.id)
        snapshots.append(snapshot.id)
        forked = arcana.snapshots.fork(snapshot.id, 2)
        self.assertEqual(len(forked.items), 2)
        for member in forked.items:
            sandboxes.append(member.id)
            self.assertEqual(member.source.value, "snapshot")
            self.assertEqual(arcana.files.read(member.id, "/workspace/sub/data.bin"), payload)
        arcana.files.write(forked.items[0].id, "/workspace/only-here", "x")
        with self.assertRaises(ArcanaError) as caught:
            arcana.files.read(forked.items[1].id, "/workspace/only-here")
        self.assertEqual(caught.exception.code, "file_not_found")


if __name__ == "__main__":
    unittest.main()
