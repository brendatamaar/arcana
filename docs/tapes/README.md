# Terminal recordings

The GIFs next to these tapes are generated, not drawn: [VHS](https://github.com/charmbracelet/vhs) types each
tape into a real shell against a throwaway Arcana on gVisor, so the output you see is what Arcana printed.

```sh
make build-all && make templates      # arcana with the dashboard, and the base and python images
scripts/record-tapes.sh               # all of them, or: scripts/record-tapes.sh doctor
```

It needs `vhs`, `ttyd`, `ffmpeg`, `jq` and a Chrome or Chromium on `PATH`. The dashboard screenshot and
recording in [`../images`](../images) come from `scripts/record-dashboard.mjs` (Playwright; see its header).
The sandbox ids in the recordings differ from run to run.
