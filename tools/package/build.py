#!/usr/bin/env python3
"""Package the release binaries for npm and PyPI.

    python3 tools/package/build.py <dist> <version> <out>

<dist> holds the release folders (rlcd-gateway_<os>_<arch>/rlcd-gateway[.exe]).
Writes <out>/npm/<package>/ for `npm publish` and <out>/pypi/*.whl for upload.

npm: `rlcd-gateway` is a small launcher whose optionalDependencies are one
package per platform (rlcd-gateway-<platform>-<arch>, with `os`/`cpu` set), so
npm installs only the binary that matches, with no postinstall download.
PyPI: one wheel per platform with the binary inside the `rlcd_gateway` package
and a console script that execs it. Standard library only.
"""

import base64
import hashlib
import json
import os
import shutil
import stat
import sys
import zipfile

REPO = "https://github.com/JimmyWesley/rlcd-gateway"
SUMMARY = ("Self-hosted LLM gateway for Claude Code, Codex, OpenCode and any "
           "OpenAI/Anthropic SDK app: routing, context pruning and a live dashboard.")
KEYWORDS = ["llm", "llm-gateway", "ai-gateway", "llm-proxy", "claude-code", "codex",
            "opencode", "openai", "anthropic", "openrouter", "ollama", "mcp"]

# Release folder -> (npm platform, npm cpu, wheel platform tags).
# The macOS floors follow the Go toolchain in gateway/go.mod (Go 1.22: 10.15 / 11.0);
# Linux builds are static (CGO_ENABLED=0), so they run on glibc and musl alike.
TARGETS = {
    "darwin_arm64": ("darwin", "arm64", ["macosx_11_0_arm64"]),
    "darwin_amd64": ("darwin", "x64", ["macosx_10_15_x86_64"]),
    "linux_amd64": ("linux", "x64", ["manylinux_2_17_x86_64", "manylinux2014_x86_64",
                                     "musllinux_1_1_x86_64"]),
    "linux_arm64": ("linux", "arm64", ["manylinux_2_17_aarch64", "manylinux2014_aarch64",
                                       "musllinux_1_1_aarch64"]),
    "windows_amd64": ("win32", "x64", ["win_amd64"]),
}

LAUNCHER = """#!/usr/bin/env node
// Runs the rlcd-gateway binary from the platform package npm installed.
const { spawnSync } = require("child_process");

const pkg = `rlcd-gateway-${process.platform}-${process.arch}`;
const exe = process.platform === "win32" ? "rlcd-gateway.exe" : "rlcd-gateway";
let bin;
try {
  bin = require.resolve(`${pkg}/bin/${exe}`);
} catch {
  console.error(`rlcd-gateway: no build for ${process.platform}-${process.arch} (${pkg} is not installed).`);
  console.error("Download one from https://github.com/JimmyWesley/rlcd-gateway/releases");
  process.exit(1);
}
const r = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
if (r.error) throw r.error;
if (r.signal) process.kill(process.pid, r.signal);
process.exit(r.status ?? 1);
"""

PY_INIT = '"""RLCD Gateway: the rlcd-gateway binary, packaged for pip."""\n\n__version__ = "{version}"\n'

PY_MAIN = '''import os
import subprocess
import sys


def binary():
    name = "rlcd-gateway.exe" if os.name == "nt" else "rlcd-gateway"
    return os.path.join(os.path.dirname(os.path.abspath(__file__)), "bin", name)


def main():
    path = binary()
    if os.name == "nt":
        try:
            sys.exit(subprocess.call([path] + sys.argv[1:]))
        except KeyboardInterrupt:
            sys.exit(130)
    os.execv(path, [path] + sys.argv[1:])


if __name__ == "__main__":
    main()
'''

PY_README = """# RLCD Gateway

{summary}

```bash
{install}
rlcd-gateway                  # proxy + dashboard on http://127.0.0.1:4777/ui/
ANTHROPIC_BASE_URL=http://127.0.0.1:4777 claude
```

This package ships the prebuilt `rlcd-gateway` binary. Documentation, screenshots
and source: {repo}
"""

NPM_INSTALL = "npm install -g rlcd-gateway   # or run it once: npx rlcd-gateway"
PIP_INSTALL = "pipx install rlcd-gateway     # or run it once: uvx rlcd-gateway"


def binary_name(folder):
    return "rlcd-gateway.exe" if folder.startswith("windows") else "rlcd-gateway"


def write(path, text, mode=0o644):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", newline="\n") as f:
        f.write(text)
    os.chmod(path, mode)


def npm_common(version):
    return {
        "version": version,
        "license": "Apache-2.0",
        "homepage": REPO,
        "repository": {"type": "git", "url": f"git+{REPO}.git"},
        "bugs": {"url": f"{REPO}/issues"},
    }


def build_npm(dist, version, out, license_text):
    root = os.path.join(out, "npm")
    optional = {}
    for folder, (plat, cpu, _) in TARGETS.items():
        name = f"rlcd-gateway-{plat}-{cpu}"
        pkg = os.path.join(root, name)
        exe = binary_name(folder)
        os.makedirs(os.path.join(pkg, "bin"))
        shutil.copy2(os.path.join(dist, f"rlcd-gateway_{folder}", exe), os.path.join(pkg, "bin", exe))
        os.chmod(os.path.join(pkg, "bin", exe), 0o755)
        write(os.path.join(pkg, "LICENSE"), license_text)
        write(os.path.join(pkg, "README.md"),
              f"# {name}\n\nThe `rlcd-gateway` binary for {plat}-{cpu}. Install "
              f"[`rlcd-gateway`](https://www.npmjs.com/package/rlcd-gateway) instead.\n")
        meta = {"name": name, **npm_common(version),
                "description": f"The rlcd-gateway binary for {plat}-{cpu}.",
                "os": [plat], "cpu": [cpu], "files": ["bin", "LICENSE", "README.md"],
                "preferUnplugged": True}
        write(os.path.join(pkg, "package.json"), json.dumps(meta, indent=2) + "\n")
        optional[name] = version

    main = os.path.join(root, "rlcd-gateway")
    write(os.path.join(main, "bin", "rlcd-gateway.js"), LAUNCHER, 0o755)
    write(os.path.join(main, "LICENSE"), license_text)
    write(os.path.join(main, "README.md"),
          PY_README.format(summary=SUMMARY, repo=REPO, install=NPM_INSTALL))
    meta = {"name": "rlcd-gateway", **npm_common(version), "description": SUMMARY,
            "keywords": KEYWORDS, "bin": {"rlcd-gateway": "bin/rlcd-gateway.js"},
            "files": ["bin", "LICENSE", "README.md"], "engines": {"node": ">=16"},
            "optionalDependencies": optional}
    write(os.path.join(main, "package.json"), json.dumps(meta, indent=2) + "\n")


def record_hash(data):
    digest = hashlib.sha256(data).digest()
    return "sha256=" + base64.urlsafe_b64encode(digest).rstrip(b"=").decode()


def build_wheel(dist, version, out, folder, tags, license_text):
    exe = binary_name(folder)
    with open(os.path.join(dist, f"rlcd-gateway_{folder}", exe), "rb") as f:
        binary = f.read()
    info = f"rlcd_gateway-{version}.dist-info"
    metadata = "\n".join([
        "Metadata-Version: 2.1",
        "Name: rlcd-gateway",
        f"Version: {version}",
        f"Summary: {SUMMARY}",
        f"Home-page: {REPO}",
        f"Project-URL: Source, {REPO}",
        f"Project-URL: Issues, {REPO}/issues",
        f"Project-URL: Releases, {REPO}/releases",
        "License: Apache-2.0",
        f"Keywords: {','.join(KEYWORDS)}",
        "Classifier: License :: OSI Approved :: Apache Software License",
        "Classifier: Environment :: Console",
        "Classifier: Intended Audience :: Developers",
        "Classifier: Topic :: Software Development",
        "Classifier: Topic :: Internet :: Proxy Servers",
        "Requires-Python: >=3.8",
        "Description-Content-Type: text/markdown",
        "",
        PY_README.format(summary=SUMMARY, repo=REPO, install=PIP_INSTALL),
    ])
    wheel = "Wheel-Version: 1.0\nGenerator: rlcd-gateway tools/package\nRoot-Is-Purelib: false\n"
    wheel += "".join(f"Tag: py3-none-{t}\n" for t in tags)
    files = [
        ("rlcd_gateway/__init__.py", PY_INIT.format(version=version).encode(), 0o644),
        ("rlcd_gateway/__main__.py", PY_MAIN.encode(), 0o644),
        (f"rlcd_gateway/bin/{exe}", binary, 0o755),
        (f"{info}/METADATA", metadata.encode(), 0o644),
        (f"{info}/WHEEL", wheel.encode(), 0o644),
        (f"{info}/entry_points.txt",
         b"[console_scripts]\nrlcd-gateway = rlcd_gateway.__main__:main\n", 0o644),
        (f"{info}/LICENSE", license_text.encode(), 0o644),
    ]
    record = "".join(f"{p},{record_hash(d)},{len(d)}\n" for p, d, _ in files)
    record += f"{info}/RECORD,,\n"
    files.append((f"{info}/RECORD", record.encode(), 0o644))

    name = f"rlcd_gateway-{version}-py3-none-{'.'.join(tags)}.whl"
    path = os.path.join(out, "pypi", name)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        for p, data, mode in files:
            zi = zipfile.ZipInfo(p, date_time=(2020, 1, 1, 0, 0, 0))
            zi.external_attr = (stat.S_IFREG | mode) << 16
            zi.compress_type = zipfile.ZIP_DEFLATED
            z.writestr(zi, data)


def main():
    if len(sys.argv) != 4:
        sys.exit(__doc__)
    dist, version, out = sys.argv[1], sys.argv[2].lstrip("v"), sys.argv[3]
    if os.path.exists(out):
        shutil.rmtree(out)
    here = os.path.dirname(os.path.abspath(__file__))
    with open(os.path.join(here, "..", "..", "LICENSE")) as f:
        license_text = f.read()
    build_npm(dist, version, out, license_text)
    for folder, (_, _, tags) in TARGETS.items():
        build_wheel(dist, version, out, folder, tags, license_text)


if __name__ == "__main__":
    main()
