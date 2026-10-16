"""Extract NUnit [TestCase(...)] arguments per test method from a C# file.

Usage: python3 -I cases.py File.cs  -> JSON {method: [[args...], ...]}
Handles "..." (with \ escapes), @"..." (verbatim, "" = quote), ints,
true/false/null and bare identifiers (enum refs, kept as strings).
"""
import json, re, sys

def parse_args(s, i):
    """Parse a comma-separated argument list starting after '('. Returns (args, index after ')')."""
    args, depth = [], 0
    while True:
        while s[i] in " \t\r\n,": i += 1
        if s[i] == ")": return args, i + 1
        if s.startswith('@"', i):
            j, out = i + 2, []
            while True:
                if s[j] == '"' and s[j+1:j+2] == '"': out.append('"'); j += 2
                elif s[j] == '"': break
                else: out.append(s[j]); j += 1
            args.append("".join(out)); i = j + 1
        elif s[i] == '"':
            j, out = i + 1, []
            esc = {"n": "\n", "t": "\t", "\\": "\\", '"': '"', "'": "'", "0": "\0"}
            while s[j] != '"':
                if s[j] == "\\":
                    if s[j+1] == "u": out.append(chr(int(s[j+2:j+6], 16))); j += 6; continue
                    out.append(esc[s[j+1]]); j += 2
                else: out.append(s[j]); j += 1
            args.append("".join(out)); i = j + 1
        elif s.startswith("new", i):
            # new[] { ... } or new string[] { ... }: a list of strings
            j = s.index("{", i); k = s.index("}", j)
            inner, _ = parse_args(s[j+1:k] + ")", 0)
            args.append(inner); i = k + 1
        else:
            m = re.compile(r"[^,)\s]+").match(s, i)
            tok = m.group(0); i = m.end()
            if re.fullmatch(r"-?\d+", tok): args.append(int(tok))
            elif tok in ("true", "false"): args.append(tok == "true")
            elif tok == "null": args.append(None)
            else: args.append(tok)
            # Named args like Description = "..." are rare; ignore anything else.

src = open(sys.argv[1], encoding="utf-8-sig").read()
out, pending = {}, []
i = 0
pat = re.compile(r"\[TestCase\(|public\s+void\s+(\w+)|//[^\n]*|/\*.*?\*/", re.S)
while True:
    m = pat.search(src, i)
    if not m: break
    if m.group(0).startswith(("//", "/*")): i = m.end(); continue
    if m.group(0).startswith("[TestCase("):
        args, i = parse_args(src, m.end())
        pending.append(args)
    else:
        if pending: out.setdefault(m.group(1), []).extend(pending)
        pending = []; i = m.end()
json.dump(out, sys.stdout, ensure_ascii=False, indent=1)
