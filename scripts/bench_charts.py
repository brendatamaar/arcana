#!/usr/bin/env python3
"""Render benchmark results as an SVG chart. Standard library only.

    python3 scripts/bench_charts.py bench/results/results.json docs/benchmarks/latency.svg

Latency metrics are drawn as p50/p95 bars on a log scale (milliseconds), with the PRD target as a
tick. Metrics without samples are listed as "no data" rather than drawn as zero.
"""

import json
import math
import sys
from xml.sax.saxutils import escape

WIDTH, ROW, LEFT, RIGHT, TOP = 760, 44, 150, 30, 56


def x_position(value: float, low: float, high: float) -> float:
    span = WIDTH - LEFT - RIGHT
    return LEFT + span * (math.log10(max(value, low)) - math.log10(low)) / (math.log10(high) - math.log10(low))


def render(report: dict) -> str:
    rows = [r for r in report["results"] if r.get("value") is None]
    values = [v for r in rows for v in (r["p50_ms"], r["p95_ms"], (r.get("target") or {}).get("limit_ms", 0)) if v]
    low, high = (min(values + [10]) * 0.8, max(values + [1000]) * 1.25) if values else (10, 10000)
    low = 10 ** math.floor(math.log10(low))
    high = 10 ** math.ceil(math.log10(high))
    height = TOP + ROW * len(rows) + 56
    out = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{WIDTH}" height="{height}" viewBox="0 0 {WIDTH} {height}" role="img" aria-label="Arcana latency by scenario">',
        "<style>text{font:12px sans-serif;fill:#222}.t{font:600 14px sans-serif}.g{stroke:#ddd}.p50{fill:#2a6fb0}.p95{fill:#9cc3e6}.tg{stroke:#c0392b;stroke-width:2}"
        "@media (prefers-color-scheme:dark){text{fill:#ddd}.g{stroke:#444}.p50{fill:#6aa9e6}.p95{fill:#2f5f8f}.tg{stroke:#ff7b6b}}</style>",
        f'<text class="t" x="{LEFT}" y="22">Latency (ms, log scale): p50 dark, p95 light, PRD target red</text>',
        f'<text x="{LEFT}" y="40">{escape(report.get("host_note") or report["server"])[:110]}</text>',
    ]
    tick = low
    while tick <= high:
        x = x_position(tick, low, high)
        out.append(f'<line class="g" x1="{x:.1f}" y1="{TOP - 8}" x2="{x:.1f}" y2="{height - 40}"/><text x="{x:.1f}" y="{height - 22}" text-anchor="middle">{tick:g}</text>')
        tick *= 10
    for i, r in enumerate(rows):
        y = TOP + i * ROW
        out.append(f'<text x="{LEFT - 8}" y="{y + 22}" text-anchor="end">{escape(r["metric"])}</text>')
        if not r["samples"]:
            out.append(f'<text x="{LEFT + 6}" y="{y + 22}">no data ({r["failures"]} failures)</text>')
            continue
        for offset, key, cls in ((4, "p50_ms", "p50"), (20, "p95_ms", "p95")):
            width = max(1.0, x_position(r[key], low, high) - LEFT)
            out.append(f'<rect class="{cls}" x="{LEFT}" y="{y + offset}" width="{width:.1f}" height="14"><title>{r["metric"]} {key} {r[key]} ms</title></rect>')
        limit = (r.get("target") or {}).get("limit_ms")
        if limit:
            x = x_position(limit, low, high)
            out.append(f'<line class="tg" x1="{x:.1f}" y1="{y}" x2="{x:.1f}" y2="{y + 36}"><title>target {limit} ms</title></line>')
    out.append("</svg>")
    return "\n".join(out) + "\n"


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    with open(sys.argv[1], encoding="utf-8") as handle:
        report = json.load(handle)
    with open(sys.argv[2], "w", encoding="utf-8") as handle:
        handle.write(render(report))
    return 0


if __name__ == "__main__":
    sys.exit(main())
