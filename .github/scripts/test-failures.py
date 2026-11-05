"""Summarize `go test -json` failures as GitHub annotations (readable without log access).

Usage: go test -json ./... | python .github/scripts/test-failures.py
Exits with go test's failure state; prints one ::error per failing package (max 10).
"""
import json
import sys
from collections import OrderedDict

output = {}  # (package, test) -> lines
failed = OrderedDict()  # package -> [tests]
running = {}  # (package, test) started without a terminal action (e.g. panic)
build_errors = []
for raw in sys.stdin:
    raw = raw.rstrip("\n")
    try:
        event = json.loads(raw)
    except ValueError:
        build_errors.append(raw)  # compiler output is not JSON
        continue
    pkg, test, action = event.get("Package", ""), event.get("Test", ""), event.get("Action")
    if action == "run":
        running[(pkg, test)] = True
    elif action in ("pass", "fail", "skip"):
        running.pop((pkg, test), None)
    if action == "output":
        output.setdefault((pkg, test), []).append(event.get("Output", "").rstrip())
    elif action == "fail":
        failed.setdefault(pkg, [])
        if test:
            failed[pkg].append(test)

def escape(text):
    return text.replace("%", "%25").replace("\r", "").replace("\n", "%0A")

annotations = []
for pkg, tests in failed.items():
    lines = []
    unfinished = [t for (p, t) in running if p == pkg]
    top = [t for t in tests if "/" not in t] + unfinished or [""]
    for test in top:
        body = [l for l in output.get((pkg, test), []) if l.strip() and not l.startswith("=== ")]
        lines.append(f"{test or '(package)'}:")
        # Unfinished tests usually panicked: show the start of the trace.
        lines.extend("  " + l.strip()[:300] for l in body[: 20 if test in unfinished else 6])
    annotations.append((pkg, "\n".join(lines)[:3500]))
if build_errors:
    annotations.insert(0, ("build", "\n".join(build_errors[:40])[:3500]))
for title, body in annotations[:10]:
    print(f"::error title={escape(title)}::{escape(body)}")

# Full report in the job summary: rendered on the public run page, no API needed.
import os
summary = os.environ.get("GITHUB_STEP_SUMMARY")
if summary and annotations:
    with open(summary, "a", encoding="utf-8") as out:
        out.write("## Failing packages\n\n")
        for title, body in annotations:
            out.write(f"### {title}\n\n```\n{body}\n```\n\n")
print(f"failed packages: {len(failed)}", file=sys.stderr)
sys.exit(1 if failed or build_errors else 0)
