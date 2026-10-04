"""Command-line flags for litsearch (Python implementation).

Same rules and messages as cmd/litsearch/flags.go: --name value, --name=value, -name,
bool flags (--flag, --flag=false); -h / --help / -help print the help.
"""

from __future__ import annotations

import re

INT = re.compile(r"[+-]?\d+")


class Opts:
    pass


def parse_flags(argv: list[str], spec: dict, help_text: str):
    """spec: {name: (default, str | int | bool)}. Raises ValueError with the message to print."""
    o = Opts()
    for k, (default, _kind) in spec.items():
        setattr(o, k.replace("-", "_"), default)
    i = 0
    while i < len(argv):
        a = argv[i]
        if a in ("-h", "--help", "-help"):
            print(help_text, end="")
            raise SystemExit(0)
        if not a.startswith("-"):
            raise ValueError(f'unexpected argument "{a}"')
        name, _, val = a.lstrip("-").partition("=")
        if name not in spec:
            raise ValueError(f"flag provided but not defined: -{name}")
        default, kind = spec[name]
        if kind is bool:
            setattr(o, name.replace("-", "_"), val.lower() not in ("false", "0") if val else True)
        else:
            if not val:
                i += 1
                if i >= len(argv):
                    raise ValueError(f"flag needs an argument: -{name}")
                val = argv[i]
            if kind is int and not INT.fullmatch(val):
                raise ValueError(f'invalid value "{val}" for flag -{name}: not an integer')
            setattr(o, name.replace("-", "_"), kind(val))
        i += 1
    return o
