#!/usr/bin/env python3
"""Extract the hand-rolled plugin examples embedded in the docs.

The docs claim their Python, Rust and Bash plugins are verified against the
protocol. This script lifts them out of the Markdown verbatim so the
conformance runner can keep that claim honest — a snippet that drifts out of
conformance fails CI instead of quietly misleading a reader.

Extraction is anchored on the section heading plus the fenced block's language,
and it fails loudly when a section moves: a silently skipped example is worse
than a broken build.

Usage: extract_doc_examples.py <output-dir>
"""

from __future__ import annotations

import os
import re
import stat
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]

# (label, markdown file, section heading, fence language, output path)
EXAMPLES = [
    ("python", "docs/plugins.md", "### A complete plugin in 20 lines", "python", "py-doc.py"),
    ("bash", "docs/plugin-sdk.md", "### Bash example", "bash", "bash-doc.sh"),
    ("rust-cargo", "docs/plugin-sdk.md", "### Rust example", "toml", "rust/Cargo.toml"),
    ("rust", "docs/plugin-sdk.md", "### Rust example", "rust", "rust/src/main.rs"),
]

EXECUTABLE = {"py-doc.py", "bash-doc.sh"}


def extract(markdown: str, heading: str, language: str) -> str:
    section = re.search(re.escape(heading) + r"(.*?)(?=\n## |\Z)", markdown, re.S)
    if not section:
        raise SystemExit(f"section not found: {heading!r}")
    block = re.search(r"```" + language + r"\n(.*?)```", section.group(1), re.S)
    if not block:
        raise SystemExit(f"no ```{language} block under {heading!r}")
    return block.group(1)


def main() -> int:
    if len(sys.argv) != 2:
        raise SystemExit("usage: extract_doc_examples.py <output-dir>")
    out_dir = Path(sys.argv[1])
    for label, doc, heading, language, relative in EXAMPLES:
        code = extract((REPO / doc).read_text(encoding="utf-8"), heading, language)
        target = out_dir / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(code, encoding="utf-8")
        if target.name in EXECUTABLE:
            target.chmod(os.stat(target).st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
        print(f"{label}: {doc} → {target}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
