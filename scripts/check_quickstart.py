"""Execute the README quickstart blocks verbatim in an isolated demo directory."""

import os
import pathlib
import re
import signal
import subprocess
import tempfile


root = pathlib.Path(__file__).resolve().parent.parent
readme = (root / "README.md").read_text(encoding="utf-8")
blocks = []
for name in ("install", "serve", "exec", "cleanup"):
    pattern = rf"<!-- quickstart:{name} -->\s*```bash\n(.*?)\n```"
    match = re.search(pattern, readme, flags=re.DOTALL)
    if match is None:
        raise SystemExit(f"missing README quickstart block: {name}")
    blocks.append(match.group(1))

header = """#!/usr/bin/env bash
set -euo pipefail
umask 077
cleanup_on_error() {
  if [ -n "${ARCANA_DEMO_PID:-}" ]; then
    if [ -n "${sandbox_id:-}" ] && [ -n "${ARCANA_API_KEY:-}" ]; then
      arcana sandbox rm "$sandbox_id" >/dev/null 2>&1 || true
    fi
    kill -TERM "$ARCANA_DEMO_PID" 2>/dev/null || true
    wait "$ARCANA_DEMO_PID" 2>/dev/null || true
  fi
}
trap cleanup_on_error EXIT
trap 'exit 1' TERM INT
"""
with tempfile.TemporaryDirectory(prefix="arcana-readme-check-") as directory:
    script = pathlib.Path(directory) / "quickstart.sh"
    script.write_text(header + "\n".join(blocks) + "\n", encoding="utf-8")
    process = subprocess.Popen(["bash", str(script)], cwd=root, start_new_session=True)
    try:
        result = process.wait(timeout=240)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGTERM)
        try:
            process.wait(timeout=45)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
        raise
    if result:
        raise SystemExit(result)
