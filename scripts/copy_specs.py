#!/usr/bin/env python3
"""Copy protocol specs from dcc-bigfred/docs and rewrite links for proto/docs."""

from pathlib import Path
import re

src_dir = Path("/home/damian/Projects/modelarstwo/dcc-bigfred/docs/content/en/specs/bigfred/protos")
dst_dir = Path("/home/damian/Projects/modelarstwo/dcc-bigfred/proto/docs")
dst_dir.mkdir(parents=True, exist_ok=True)

DOCS = "https://github.com/dcc-bigfred/docs/blob/main/content/en"

replacements = [
    ("../../../pkgs/loco/commandstation/", "../go/commandstation/"),
    ("../../../pkgs/loco/commandstation", "../go/commandstation"),
    ("../architecture/", f"{DOCS}/specs/bigfred/architecture/"),
    ("../plans/", f"{DOCS}/specs/bigfred/plans/"),
    ("../../../related/", f"{DOCS}/related/"),
    ("../../hardware/", f"{DOCS}/specs/hardware/"),
]

intro_loconet = (
    "> Technical reference for the **LocoNet®** bus as used by BigFred's\n"
    "> `loconet_serial` and `loconet_tcp` command-station drivers\n"
    "> ([`pkgs/loco/commandstation/loconet.go`](../../../pkgs/loco/commandstation/loconet.go)).\n"
)
intro_loconet_new = (
    "> Technical reference for the **LocoNet®** bus.\n"
    "> Implementation: [`go/commandstation`](../go/commandstation).\n"
)

for name in ("loconet.md", "z21.md", "withrottle.md"):
    text = (src_dir / name).read_text()
    if name == "loconet.md":
        text = text.replace(intro_loconet, intro_loconet_new, 1)
    for old, new in replacements:
        text = text.replace(old, new)
    (dst_dir / name).write_text(text)
    print(f"wrote {name} ({len(text)} bytes)")
