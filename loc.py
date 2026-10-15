#!/usr/bin/env python3
"""
loc.py - count lines of code, comments, and blanks in a source tree.

Usage:
    python3 loc.py [paths...] [options]

Options:
    --ext .py,.js      only count these extensions (comma separated)
    --exclude DIR      skip a directory name (repeatable)
    --by-file          print a line per file instead of just totals
    --sort code        sort the by-file report: code | comment | blank | name
    --json             machine-readable output
    -q, --quiet        totals only

Comment syntax is detected per language. Supports line comments (//, #, --, %)
and block comments (C-style /* */, HTML <!-- -->, and Python/Ruby triple quotes).
"""

import argparse
import json
import os
import sys

# ---------------------------------------------------------------- languages

# ext -> (line_comment_tokens, block_comment_pairs, string_delims_that_act_as_blocks)
LANGS = {
    ".py":    (("#",),                  (),                              ('"""', "'''")),
    ".pyw":   (("#",),                  (),                              ('"""', "'''")),
    ".rb":    (("#",),                  (),                              ("=begin", "=end")),
    ".js":    (("//",),                 (("/*", "*/"),),                 ()),
    ".jsx":   (("//",),                 (("/*", "*/"),),                 ()),
    ".ts":    (("//",),                 (("/*", "*/"),),                 ()),
    ".tsx":   (("//",),                 (("/*", "*/"),),                 ()),
    ".c":     (("//",),                 (("/*", "*/"),),                 ()),
    ".h":     (("//",),                 (("/*", "*/"),),                 ()),
    ".cc":    (("//",),                 (("/*", "*/"),),                 ()),
    ".cpp":   (("//",),                 (("/*", "*/"),),                 ()),
    ".hpp":   (("//",),                 (("/*", "*/"),),                 ()),
    ".cs":    (("//",),                 (("/*", "*/"),),                 ()),
    ".java":  (("//",),                 (("/*", "*/"),),                 ()),
    ".kt":    (("//",),                 (("/*", "*/"),),                 ()),
    ".go":    (("//",),                 (("/*", "*/"),),                 ()),
    ".rs":    (("//",),                 (("/*", "*/"),),                 ()),
    ".swift": (("//",),                 (("/*", "*/"),),                 ()),
    ".php":   (("//", "#"),             (("/*", "*/"),),                 ()),
    ".scala": (("//",),                 (("/*", "*/"),),                 ()),
    ".sh":    (("#",),                  (),                              ()),
    ".bash":  (("#",),                  (),                              ()),
    ".zsh":   (("#",),                  (),                              ()),
    ".pl":    (("#",),                  (),                              ()),
    ".r":     (("#",),                  (),                              ()),
    ".sql":   (("--",),                 (("/*", "*/"),),                 ()),
    ".lua":   (("--",),                 (("--[[", "]]"),),               ()),
    ".hs":    (("--",),                 (("{-", "-}"),),                 ()),
    ".erl":   (("%",),                  (),                              ()),
    ".ex":    (("#",),                  (),                              ()),
    ".exs":   (("#",),                  (),                              ()),
    ".html":  ((),                      (("<!--", "-->"),),              ()),
    ".htm":   ((),                      (("<!--", "-->"),),              ()),
    ".xml":   ((),                      (("<!--", "-->"),),              ()),
    ".css":   ((),                      (("/*", "*/"),),                 ()),
    ".scss":  (("//",),                 (("/*", "*/"),),                 ()),
    ".yaml":  (("#",),                  (),                              ()),
    ".yml":   (("#",),                  (),                              ()),
    ".toml":  (("#",),                  (),                              ()),
    ".ini":   (("#", ";"),              (),                              ()),
    ".cfg":   (("#", ";"),              (),                              ()),
    ".vim":   (('"',),                  (),                              ()),
    ".m":     (("%",),                  (("/*", "*/"),),                 ()),
    ".tex":   (("%",),                  (),                              ()),
}

DEFAULT_EXCLUDES = {
    ".git", ".hg", ".svn", "node_modules", "__pycache__", ".mypy_cache",
    ".pytest_cache", ".tox", "venv", ".venv", "env", "dist", "build",
    "target", "vendor", ".idea", ".vscode", ".next", ".cache", "bower_components",
}

BINARY_EXT = {
    ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".ico", ".svgz", ".webp",
    ".pdf", ".zip", ".gz", ".bz2", ".xz", ".tar", ".7z", ".rar",
    ".exe", ".dll", ".so", ".dylib", ".bin", ".o", ".a", ".class", ".pyc",
    ".woff", ".woff2", ".ttf", ".otf", ".eot", ".mp3", ".mp4", ".mov",
    ".wav", ".avi", ".mkv", ".webm", ".db", ".sqlite", ".sqlite3", ".lock",
}

MAX_BYTES = 4 * 1024 * 1024  # skip anything bigger than this


# ------------------------------------------------------------------ counting

def count_file(path, lang):
    """Return (code, comment, blank) for one file. Raises OSError on read fail."""
    line_tokens, block_pairs, triple_strs = lang
    code = comment = blank = 0

    with open(path, "r", encoding="utf-8", errors="replace") as fh:
        text = fh.read(MAX_BYTES)

    in_block = None          # the closing token we're waiting for, or None
    pending_string = None    # open triple-quote delimiter, if any

    for raw in text.splitlines():
        stripped = raw.strip()

        if not stripped:
            blank += 1
            continue

        # -------- inside a block comment: whole line counts as a comment
        if in_block is not None and pending_string is None:
            comment += 1
            if in_block in stripped:
                # close it; anything after may be code (approximate)
                in_block = None
            continue

        # -------- inside a python-style docstring
        if pending_string is not None:
            comment += 1
            if pending_string in stripped:
                pending_string = None
            continue

        # -------- normalize the leading part of the line
        rest = stripped
        has_code = False
        has_comment = False

        while rest:
            # line comment?
            for tok in line_tokens:
                if rest.startswith(tok):
                    has_comment = True
                    rest = ""
                    break
            if not rest:
                break

            # triple-quoted / heredoc-ish string?
            matched_string = None
            for delim in triple_strs:
                if rest.startswith(delim):
                    matched_string = delim
                    break
            if pending_string is None and matched_string:
                if rest.count(matched_string) >= 2:
                    rest = rest[rest.find(matched_string, 1) + len(matched_string):]
                    has_comment = True          # docstring line
                    continue
                pending_string = matched_string
                has_comment = True
                rest = ""
                break

            # block comment?
            matched_block = None
            for opener, closer in block_pairs:
                if rest.startswith(opener):
                    matched_block = (opener, closer)
                    break
            if matched_block:
                opener, closer = matched_block
                if closer in rest[len(opener):]:
                    after = rest.index(closer, len(opener)) + len(closer)
                    if rest[:after].strip() != opener.strip():
                        pass  # code before comment on same line stays counted
                    rest = rest[after:]
                    has_comment = True
                    continue
                in_block = closer
                has_comment = True
                rest = ""
                break

            # otherwise this is real code; consume one token chunk
            has_code = True
            nxt = len(rest)
            for tok in line_tokens:
                i = rest.find(tok, 1)
                if i != -1:
                    nxt = min(nxt, i)
            rest = rest[nxt:]

        if has_code:
            code += 1
        elif has_comment:
            comment += 1
        else:
            code += 1

    return code, comment, blank


# ------------------------------------------------------------------ walking

def iter_files(paths, excludes, allowed_ext):
    seen = set()
    for root_path in paths:
        if os.path.isfile(root_path):
            yield root_path
            continue
        for root, dirs, files in os.walk(root_path):
            dirs[:] = [d for d in dirs if d not in excludes and not d.startswith(".git")]
            for name in sorted(files):
                ext = os.path.splitext(name)[1].lower()
                if ext in BINARY_EXT:
                    continue
                if allowed_ext and ext not in allowed_ext:
                    continue
                full = os.path.join(root, name)
                real = os.path.realpath(full)
                if real in seen:
                    continue
                seen.add(real)
                try:
                    if os.path.getsize(full) > MAX_BYTES:
                        continue
                except OSError:
                    continue
                yield full


def ext_lang(path):
    ext = os.path.splitext(path)[1].lower()
    if ext in LANGS:
        return LANGS[ext]
    # unknown text file: no comment syntax, count every non-blank line as code
    return ((), (), ())


# ------------------------------------------------------------------ main

def main(argv=None):
    ap = argparse.ArgumentParser(description="Count lines of code.")
    ap.add_argument("paths", nargs="*", default=None,
                    help="files or directories (default: current directory)")
    ap.add_argument("--ext", help="comma separated extensions, e.g. .py,.js")
    ap.add_argument("--exclude", action="append", default=[],
                    help="extra directory name to skip (repeatable)")
    ap.add_argument("--by-file", action="store_true", help="per-file breakdown")
    ap.add_argument("--sort", default="code",
                    choices=["code", "comment", "blank", "name"])
    ap.add_argument("--json", action="store_true", help="JSON output")
    ap.add_argument("-q", "--quiet", action="store_true", help="totals only")
    args = ap.parse_args(argv)

    targets = args.paths or ["."]
    excludes = DEFAULT_EXCLUDES | set(args.exclude)
    allowed = None
    if args.ext:
        allowed = {e if e.startswith(".") else "." + e
                   for e in (x.strip().lower() for x in args.ext.split(",")) if e}

    per_file = []
    for path in iter_files(targets, excludes, allowed):
        try:
            code, comment, blank = count_file(path, ext_lang(path))
        except OSError:
            continue
        per_file.append({"name": path, "code": code,
                         "comment": comment, "blank": blank,
                         "total": code + comment + blank})

    tot_code = sum(f["code"] for f in per_file)
    tot_comment = sum(f["comment"] for f in per_file)
    tot_blank = sum(f["blank"] for f in per_file)
    tot = tot_code + tot_comment + tot_blank

    if args.json:
        print(json.dumps({
            "files": len(per_file),
            "code": tot_code, "comment": tot_comment,
            "blank": tot_blank, "total": tot,
            "by_file": per_file,
        }, indent=2))
        return 0

    if args.by_file:
        key = {"name": lambda f: f["name"],
               "code": lambda f: -f["code"],
               "comment": lambda f: -f["comment"],
               "blank": lambda f: -f["blank"]}[args.sort]
        rows = sorted(per_file, key=key)
        w = max([len(f["name"]) for f in rows] + [4])
        print(f"{'file':<{w}} {'code':>7} {'comment':>8} {'blank':>6} {'total':>7}")
        print("-" * (w + 32))
        for f in rows:
            print(f"{f['name']:<{w}} {f['code']:>7} {f['comment']:>8} "
                  f"{f['blank']:>6} {f['total']:>7}")
        print("-" * (w + 32))

    if not args.quiet or not args.by_file:
        lang_label = ", ".join(sorted(allowed)) if allowed else "all files"
        print(f"files      : {len(per_file)}")
        print(f"language   : {lang_label}")
        print(f"code       : {tot_code}")
        print(f"comment    : {tot_comment}")
        print(f"blank      : {tot_blank}")
        print(f"total      : {tot}")
        if tot:
            print(f"comment %  : {tot_comment / tot * 100:.1f}%")

    return 0


if __name__ == "__main__":
    sys.exit(main())
