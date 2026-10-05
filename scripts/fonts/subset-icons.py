#!/usr/bin/env python3
"""Subset the self-hosted Material Symbols Rounded font (decision 4g).

The Staff App and the Member App render icons with the Material Symbols
ligature font: the text "home" in a .material-symbols-rounded span becomes the
home glyph. Offline devices (ops tablet, POS, warehouse, ESS, Member App PWA)
cannot reach Google Fonts, so the apps ship their own copy, precached by the
service worker. The full variable font is 5.4 MB; this script keeps only the
icons the code uses:

  1. web sources (web/apps/**, web/packages/**): every string literal that is
     a Material Symbols name — <Icon name="...">, icon props and fields,
     ternaries, tile tuples ['icon', 'Label', '/path'] and lookup tables all
     reduce to a quoted name somewhere. Excluded: tests, the Morphic showcase
     (it loads the full font from Google), the public website (it uses no
     icons) and the generated API client.
  2. Go sources: navigation items (internal/platform/navigation, Icon: "..."
     and the live/m/mod helpers) and every other Icon: "..." field, e.g. the
     ESS sections.
  3. scripts/fonts/icon-allowlist.txt: the curated set for user-configurable
     icon fields.

It writes the subset font (all four variation axes FILL, GRAD, opsz, wght;
the rlig ligatures and the rclt FILL alternates) and the JSON list of the
icons it contains. web/packages/shell/src/icons.test.ts and
internal/app/icons_test.go fail when code names an icon missing from that
list — run this script again and commit both outputs.

Requirements (not a repository dependency):  pip install fonttools brotli

Usage, from anywhere in the repository:

  python3 scripts/fonts/subset-icons.py           # write font + JSON
  python3 scripts/fonts/subset-icons.py --check   # exit 1 if the JSON is stale

Why not `pyftsubset --text=<names>`: every icon ligature is made of the same
letters a-z 0-9 _, so the GSUB closure of the text keeps all 4,000+ icons.
The script passes the icon glyphs explicitly and turns layout closure off,
which keeps exactly the ligature rules of the kept glyphs.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FONT = ROOT / "design-system/fonts/material-symbols/MaterialSymbolsRounded[FILL,GRAD,opsz,wght].woff2"
ALLOWLIST = ROOT / "scripts/fonts/icon-allowlist.txt"
OUT_DIR = ROOT / "web/packages/shell/src/assets"
OUT_FONT = OUT_DIR / "material-symbols-rounded.subset.woff2"
OUT_JSON = OUT_DIR / "material-symbols-rounded.icons.json"

WEB_ROOTS = [ROOT / "web/apps", ROOT / "web/packages"]
WEB_EXCLUDE_DIRS = {"node_modules", "dist", ".next"}
WEB_EXCLUDE = (
    "web/apps/web/",                          # public website: no icon font
    "web/packages/ui/showcase/",              # Morphic showcase: full font from Google
    "web/packages/api-client/src/schema.ts",  # generated types
)
TEST_FILE = re.compile(r"\.(test|spec)\.tsx?$")

# Any quoted lowercase name; kept only when it is a Material Symbols name.
LITERAL = re.compile(r"""(['"`])([a-z0-9][a-z0-9_]*)\1""")
# Places that must name an icon: an unknown name there is a typo that would
# render as text. Mirrored by web/packages/shell/src/icons.test.ts.
# (pattern, whether the match is an expression whose literals are the names)
WEB_ICON_CONTEXTS = [
    (re.compile(r"""<Icon\b[^>]*?\bname=(?:"([^"]*)"|'([^']*)')"""), False),
    (re.compile(r"""<Icon\b[^>]*?\bname=\{([^{}]*)\}"""), True),
    (re.compile(r"""\b(?:icon|[A-Za-z_]\w*Icon)\s*[:=]\s*\{?\s*(?:"([^"]*)"|'([^']*)')"""), False),
]
# Operands of comparisons inside an icon expression (sortOrder === 'asc' ? ...).
COMPARISON = re.compile(r"""[!=]==?\s*(['"`])[^'"`]*\1|(['"`])[^'"`]*\2\s*[!=]==?""")
GO_ICON =re.compile(r"""\bIcon:\s*"([^"]*)\"""")
GO_NAV_HELPER = re.compile(r"""\b(?:live|m|mod)\(\s*"[^"]*",\s*"[^"]*",\s*"([^"]*)\"""")


def rel(p: Path) -> str:
    return p.relative_to(ROOT).as_posix()


def font_icons(font) -> dict[str, str]:
    """Maps every ligature name of the font to its glyph."""
    char_of = {}
    for cp, glyph in sorted(font.getBestCmap().items(), reverse=True):
        char_of[glyph] = chr(cp).lower()
    names: dict[str, str] = {}
    gsub = font["GSUB"].table
    for record in gsub.FeatureList.FeatureRecord:
        if record.FeatureTag not in ("liga", "rlig"):
            continue
        for index in record.Feature.LookupListIndex:
            for sub in gsub.LookupList.Lookup[index].SubTable:
                sub = getattr(sub, "ExtSubTable", sub)
                for first, ligatures in getattr(sub, "ligatures", {}).items():
                    for lig in ligatures:
                        name = "".join(char_of[g] for g in [first, *lig.Component])
                        names[name] = lig.LigGlyph
    return names


def single_substitutions(font) -> dict[str, set[str]]:
    """glyph -> alternates (the .fill glyphs that rclt swaps in when FILL > 0)."""
    out: dict[str, set[str]] = {}
    for lookup in font["GSUB"].table.LookupList.Lookup:
        for sub in lookup.SubTable:
            sub = getattr(sub, "ExtSubTable", sub)
            for src, dst in getattr(sub, "mapping", {}).items():
                out.setdefault(src, set()).add(dst)
    return out


def web_files():
    for base in WEB_ROOTS:
        for p in sorted(base.rglob("*")):
            r = rel(p)
            if p.suffix not in (".ts", ".tsx") or not p.is_file() or TEST_FILE.search(r):
                continue
            if WEB_EXCLUDE_DIRS.intersection(r.split("/")) or r.startswith(WEB_EXCLUDE):
                continue
            yield p


def scan(icons: dict[str, str]) -> tuple[set[str], list[str]]:
    used: set[str] = set()
    errors: list[str] = []

    def must(name: str, where: str):
        if name in icons:
            used.add(name)
        else:
            errors.append(f"{where}: '{name}' is not a Material Symbols Rounded icon")

    for p in web_files():
        text = p.read_text(encoding="utf-8")
        used.update(m.group(2) for m in LITERAL.finditer(text) if m.group(2) in icons)
        for rx, expression in WEB_ICON_CONTEXTS:
            for m in rx.finditer(text):
                value = next(g for g in m.groups() if g is not None)
                line = text.count("\n", 0, m.start()) + 1
                literals = [x.group(2) for x in LITERAL.finditer(COMPARISON.sub("", value))] if expression else [value]
                for name in literals:
                    if name and not name.startswith("/"):
                        must(name, f"{rel(p)}:{line}")

    for p in sorted((ROOT / "internal").rglob("*.go")):
        if p.name.endswith("_test.go"):
            continue
        text = p.read_text(encoding="utf-8")
        patterns = [GO_ICON] + ([GO_NAV_HELPER] if "platform/navigation" in rel(p) else [])
        for rx in patterns:
            for m in rx.finditer(text):
                if m.group(1):
                    must(m.group(1), f"{rel(p)}:{text.count(chr(10), 0, m.start()) + 1}")

    for n, line in enumerate(ALLOWLIST.read_text(encoding="utf-8").splitlines(), 1):
        name = line.split("#", 1)[0].strip()
        if name:
            must(name, f"{rel(ALLOWLIST)}:{n}")
    return used, errors


def write_json(names: list[str]) -> str:
    return json.dumps({
        "$comment": "Generated by scripts/fonts/subset-icons.py from the Material Symbols Rounded font "
                    "(Apache-2.0, design-system/fonts/material-symbols). Do not edit; re-run the script.",
        "font": OUT_FONT.name,
        "icons": names,
    }, indent=2) + "\n"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--check", action="store_true", help="only verify that the committed icon list is current")
    args = ap.parse_args()
    try:
        from fontTools import subset
        from fontTools.ttLib import TTFont
    except ImportError:
        print("fontTools is required: pip install fonttools brotli", file=sys.stderr)
        return 2

    font = TTFont(FONT, recalcTimestamp=False)
    icons = font_icons(font)
    used, errors = scan(icons)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    names = sorted(used)
    expected = write_json(names)

    if args.check:
        current = OUT_JSON.read_text(encoding="utf-8") if OUT_JSON.exists() else ""
        if current != expected:
            print(f"{rel(OUT_JSON)} is stale: run python3 scripts/fonts/subset-icons.py", file=sys.stderr)
            return 1
        print(f"{len(names)} icons, {rel(OUT_JSON)} is current")
        return 0

    alternates = single_substitutions(font)
    glyphs = {".notdef"}
    for name in names:
        glyphs.add(icons[name])
        glyphs.update(alternates.get(icons[name], ()))
    chars = sorted({c for name in names for c in name})

    options = subset.Options()
    options.flavor = "woff2"
    options.layout_features = ["*"]   # rlig (names -> icons), rclt (FILL alternates)
    options.layout_closure = False    # keep the given glyphs only, see the module doc
    options.name_IDs = ["*"]          # keep copyright and licence records
    options.notdef_outline = True
    options.glyph_names = False
    subsetter = subset.Subsetter(options)
    subsetter.populate(glyphs=sorted(glyphs), unicodes=[ord(c) for c in chars])
    subsetter.subset(font)

    OUT_DIR.mkdir(parents=True, exist_ok=True)
    subset.save_font(font, str(OUT_FONT), options)
    OUT_JSON.write_text(expected, encoding="utf-8")

    check = TTFont(OUT_FONT)
    axes = [a.axisTag for a in check["fvar"].axes]
    missing = sorted(set(names) - set(font_icons(check)))
    if missing or sorted(axes) != ["FILL", "GRAD", "opsz", "wght"]:
        print(f"subset is broken: axes {axes}, missing ligatures {missing[:10]}", file=sys.stderr)
        return 1
    size = OUT_FONT.stat().st_size
    print(f"{len(names)} icons, {len(glyphs)} glyphs, axes {','.join(axes)}: "
          f"{rel(OUT_FONT)} {size / 1024:.1f} KiB (full font {FONT.stat().st_size / 1024:.0f} KiB)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
