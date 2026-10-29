#!/usr/bin/env python3
"""Genera THIRD_PARTY_NOTICES.md con los avisos de licencia de lo que se distribuye.

Go: módulos enlazados en el binario, en macOS, Linux y Windows (cada sistema enlaza módulos distintos).
npm: dependencias de producción de frontend/ (requiere `npm ci`).
Uso: python3 scripts/gen-third-party-notices.py   (desde cualquier carpeta)
"""
import json
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
LICENSE_NAMES = ("license", "licence", "copying", "unlicense")


def run(cmd, cwd=ROOT, env=None):
    return subprocess.run(cmd, cwd=cwd, env=env, check=True, capture_output=True, text=True).stdout


def find_license(d: Path):
    if not d.is_dir():
        return None
    for f in sorted(d.iterdir()):
        if f.is_file() and f.name.lower().split(".")[0] in LICENSE_NAMES:
            return f.read_text(errors="replace").strip()
    return None


def go_modules():
    mods = {}
    for goos in ("darwin", "linux", "windows"):
        env = {**os.environ, "GOOS": goos, "CGO_ENABLED": "1"}
        out = run(["go", "list", "-deps", "-f",
                   "{{with .Module}}{{if not .Main}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}{{end}}", "."], env=env)
        for line in out.splitlines():
            if line:
                path, ver, d = line.split("|")
                mods[(path, ver)] = Path(d)
    return sorted(mods.items())


def npm_packages():
    out = run(["npm", "ls", "--omit=dev", "--all", "--parseable", "--long"], cwd=ROOT / "frontend")
    pkgs = {}
    for line in out.splitlines():
        d, _, spec = line.partition(":")
        if "node_modules" not in d:
            continue  # la raíz del propio frontend
        pj = json.loads((Path(d) / "package.json").read_text())
        lic = pj.get("license") or pj.get("licenses") or "?"
        if isinstance(lic, dict):
            lic = lic.get("type", "?")
        pkgs[(pj["name"], pj["version"])] = (Path(d), str(lic))
    return sorted(pkgs.items())


def section(title, text):
    return f"### {title}\n\n```text\n{text}\n```\n\n"


def main():
    missing = []
    out = ["# Avisos de terceros\n\n",
           "Dbacus incluye software de terceros bajo las licencias que se reproducen abajo. "
           "Este archivo se genera con `scripts/gen-third-party-notices.py`; no lo edites a mano.\n\n",
           "## Módulos de Go\n\n"]
    for (path, ver), d in go_modules():
        text = find_license(d)
        if not text:
            missing.append(f"go:{path}")
            text = "(sin archivo de licencia en el módulo; consulta el repositorio del proyecto)"
        out.append(section(f"{path} {ver}", text))
    out.append("## Paquetes de npm (dependencias de producción del frontend)\n\n")
    for (name, ver), (d, lic) in npm_packages():
        text = find_license(d)
        if not text:
            missing.append(f"npm:{name}")
            text = f"(sin archivo de licencia en el paquete; licencia declarada: {lic})"
        out.append(section(f"{name} {ver} — {lic}", text))
    (ROOT / "THIRD_PARTY_NOTICES.md").write_text("".join(out))
    n = sum(1 for s in out if s.startswith("### "))
    print(f"{n} componentes; sin archivo de licencia: {missing or 'ninguno'}")


if __name__ == "__main__":
    sys.exit(main())
