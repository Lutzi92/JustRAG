#!/usr/bin/env python3
"""Download the Google Fonts woff2 files and emit a self-hosted @font-face sheet.

Keeps every subset and its unicode-range, exactly mirroring what Google serves,
so the browser still downloads only the subsets a page actually needs.
"""
import re
import subprocess
import sys
from pathlib import Path

SRC_CSS = Path(sys.argv[1])
OUT_DIR = Path(sys.argv[2])
OUT_DIR.mkdir(parents=True, exist_ok=True)

css = SRC_CSS.read_text()

# Each @font-face is preceded by a /* subset */ comment.
pattern = re.compile(
    r"/\*\s*(?P<subset>[a-z0-9-]+)\s*\*/\s*"
    r"@font-face\s*\{(?P<body>[^}]*)\}",
    re.MULTILINE,
)

def field(body: str, name: str) -> str:
    m = re.search(rf"{name}:\s*([^;]+);", body)
    return m.group(1).strip() if m else ""

def slug(family: str) -> str:
    return family.strip("'\"").lower().replace(" ", "-")

faces = []
for m in pattern.finditer(css):
    body = m.group("body")
    subset = m.group("subset")
    family = field(body, "font-family")
    weight = field(body, "font-weight")
    style = field(body, "font-style")
    urange = field(body, "unicode-range")
    url = re.search(r"url\((https://[^)]+\.woff2)\)", body).group(1)

    name = f"{slug(family)}-{subset}.woff2"
    dest = OUT_DIR / name
    if not dest.exists():
        subprocess.run(["curl", "-sSfL", url, "-o", str(dest)], check=True)
    faces.append((family, style, weight, name, urange, subset))

header = """/* Self-hosted web fonts — generated, do not hand-edit.
 *
 * Previously loaded from fonts.googleapis.com. Serving them from our own
 * origin removes two failure modes:
 *
 *   1. Consistency. Any network that blocks or slows Google's CDN — including
 *      the egress proxy in front of the production cluster — silently drops
 *      users to system fallback fonts, so the app renders differently for
 *      them than for everyone else.
 *   2. Data protection. Requesting fonts from Google discloses each visitor's
 *      IP to a third party on every page load, which German courts have held
 *      to require consent (LG Munich I, 3 O 17493/20).
 *
 * These files are also cached far better than before: Vite fingerprints them
 * into /assets, which is the one path served immutable for a year.
 *
 * Every subset Google publishes is kept, together with its unicode-range, so
 * the browser still fetches only the subsets a page actually needs.
 *
 * Inter, Manrope and JetBrains Mono are all SIL Open Font License 1.1.
 * Regenerate with scripts/localize-fonts.py (see the header there).
 */
"""

lines = [header]
for family, style, weight, name, urange, subset in faces:
    lines.append(
        f"/* {slug(family)} · {subset} */\n"
        "@font-face {\n"
        f"  font-family: {family};\n"
        f"  font-style: {style};\n"
        f"  font-weight: {weight};\n"
        "  font-display: swap;\n"
        f"  src: url('./{name}') format('woff2');\n"
        f"  unicode-range: {urange};\n"
        "}\n"
    )

(OUT_DIR / "fonts.css").write_text("\n".join(lines))
print(f"{len(faces)} faces -> {OUT_DIR}")
