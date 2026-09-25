#!/usr/bin/env python3
"""Refresh the offline handbook from an exact upstream Mermaid release.

Development-only command: python3 internal/handbook/sync.py 12.0.0
No syntax content is written by lazymermaid; Markdown is copied verbatim.
"""
import concurrent.futures
import json
from pathlib import Path
import re
import sys
import urllib.parse
import urllib.request


def fetch(url):
    request = urllib.request.Request(url, headers={"User-Agent": "lazymermaid-handbook-sync"})
    with urllib.request.urlopen(request, timeout=40) as response:
        return response.read()


def main():
    version = sys.argv[1] if len(sys.argv) > 1 else "12.0.0"
    if not re.fullmatch(r"\d+\.\d+\.\d+(?:-[\w.-]+)?", version):
        raise SystemExit("Expected an exact Mermaid release version")
    tag = "mermaid@" + version
    ref = urllib.parse.quote(tag, safe="")
    root = Path(__file__).resolve().parent
    tree = json.loads(fetch(f"https://api.github.com/repos/mermaid-js/mermaid/git/trees/{ref}?recursive=1"))
    prefix = "packages/mermaid/src/docs/"
    selected = sorted(entry["path"] for entry in tree["tree"] if entry["type"] == "blob" and (
        (entry["path"].startswith(prefix + "syntax/") and entry["path"].endswith(".md")) or
        entry["path"] in [prefix + "intro/syntax-reference.md", prefix + "config/theming.md", prefix + "config/directives.md"]
    ))
    if not selected:
        raise SystemExit("No upstream documentation found; existing snapshot untouched")
    raw = f"https://raw.githubusercontent.com/mermaid-js/mermaid/{ref}/"

    def download(source):
        data = fetch(raw + source)
        text = data.decode("utf-8")
        heading = re.search(r"^#\s+(.+)$", text, re.M)
        slug = Path(source).stem
        return data, {"slug": slug, "title": heading.group(1).strip() if heading else slug,
                      "version": version,
                      "source": f"https://github.com/mermaid-js/mermaid/blob/{ref}/{source}"}

    with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
        documents = list(pool.map(download, selected))
    license_data = fetch(raw + "LICENSE")
    docs = root / "docs"
    docs.mkdir(parents=True, exist_ok=True)
    for old in docs.glob("*.md"):
        old.unlink()
    for data, metadata in documents:
        (docs / (metadata["slug"] + ".md")).write_bytes(data)
    (root / "LICENSE.upstream").write_bytes(license_data)
    (root / "metadata.json").write_text(json.dumps({"version": version, "tag": tag,
        "pages": [metadata for _, metadata in documents]}, indent=2, ensure_ascii=False) + "\n")
    print(f"Mirrored {len(documents)} upstream pages from {tag}")


if __name__ == "__main__":
    main()
