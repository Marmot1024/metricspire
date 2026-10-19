#!/usr/bin/env python3
"""Check tracked Markdown without network access or third-party dependencies."""

from collections import Counter
import json
from pathlib import Path
import re
import subprocess
import sys
from urllib.parse import unquote, urlsplit


ROOT = Path(__file__).resolve().parents[1]


def prose_and_json(text, path, errors):
    """Blank fenced code before checking prose; parse complete JSON examples."""
    lines = text.splitlines()
    prose = []
    fence = None
    code = []
    for number, line in enumerate(lines, 1):
        marker = re.match(r"^\s*(`{3,}|~{3,})(.*)$", line)
        if fence is None and marker:
            fence = (marker[1][0], len(marker[1]), marker[2].strip(), number)
            code = []
            prose.append("")
        elif fence is not None:
            if marker and marker[1][0] == fence[0] and len(marker[1]) >= fence[1] and not marker[2].strip():
                if fence[2] == "json":
                    try:
                        json.loads("\n".join(code))
                    except ValueError as error:
                        errors.append(f"{path}:{fence[3]}: invalid JSON example: {error}")
                fence = None
            else:
                code.append(line)
            prose.append("")
        else:
            prose.append(line)
    if fence is not None:
        errors.append(f"{path}:{fence[3]}: unclosed code fence")
    return "\n".join(prose)


def heading_ids(prose):
    ids = set(re.findall(r'\bid=["\']([^"\']+)["\']', prose))
    used = Counter()
    for heading in re.findall(r"^#{1,6}\s+(.+?)\s*#*\s*$", prose, re.MULTILINE):
        heading = re.sub(r"<[^>]+>", "", heading).lower()
        slug = re.sub(r"[^\w\- ]", "", heading).replace(" ", "-")
        suffix = used[slug]
        candidate = slug if suffix == 0 else f"{slug}-{suffix}"
        while candidate in ids:
            suffix += 1
            candidate = f"{slug}-{suffix}"
        used[slug] = suffix + 1
        ids.add(candidate)
    return ids


def main():
    paths = subprocess.check_output(
        ["git", "ls-files", "-z", "--", "*.md"], cwd=ROOT
    ).decode().split("\0")
    paths = [path for path in paths if path and (ROOT / path).is_file()]
    errors = []
    prose = {
        path: prose_and_json((ROOT / path).read_text(), path, errors)
        for path in paths
    }
    anchors = {path: heading_ids(text) for path, text in prose.items()}
    for path, text in prose.items():
        links = re.findall(r'!?\[[^\]]*\]\(<?([^\s)>]+)>?(?:\s+"[^"]*")?\)', text)
        links += re.findall(r'(?:href|src)=["\']([^"\']+)["\']', text)
        for link in links:
            url = urlsplit(link)
            if url.scheme or url.netloc or link.startswith("/"):
                continue
            target = ((ROOT / path).parent / unquote(url.path)).resolve() if url.path else ROOT / path
            if not target.exists():
                errors.append(f"{path}: missing local target: {link}")
                continue
            if url.fragment and target.suffix == ".md":
                try:
                    key = target.relative_to(ROOT).as_posix()
                except ValueError:
                    errors.append(f"{path}: Markdown target outside repository: {link}")
                    continue
                if key not in anchors or unquote(url.fragment) not in anchors[key]:
                    errors.append(f"{path}: missing Markdown heading: {link}")
        paragraphs = [" ".join(p.split()) for p in re.split(r"\n\s*\n", text)]
        for paragraph, count in Counter(paragraphs).items():
            if count > 1 and len(paragraph) >= 160 and not re.match(r"[#>|<*\-\d]", paragraph):
                errors.append(f"{path}: repeated prose ({count} copies): {paragraph[:75]}…")
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print(f"Documentation checks passed: {len(paths)} Markdown files (local links, headings, JSON and repeated prose).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
