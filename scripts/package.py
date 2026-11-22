#!/usr/bin/env python3
"""WSL/Linuxで初期配布アーカイブを作成する。例: python3 scripts/package.py --output-dir .local/dist"""
import argparse
import gzip
import hashlib
import io
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import zipfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", default=".local/dist", help="新規の出力フォルダ（既存は拒否）。リポジトリ内ではGit除外済みのディレクトリを使用してください。")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    go_env = dict(os.environ, GOWORK="off")
    version = subprocess.check_output(["go", "env", "GOVERSION"], cwd=root, env=go_env, text=True).strip()
    if version != "go1.27.4":
        parser.error("通知の監査対象はgo1.27.4です。対応するGoを使用するか、通知を再確認してください。")
    out = Path(args.output_dir).resolve()
    out.mkdir(parents=True, exist_ok=False)
    checksums = []
    with tempfile.TemporaryDirectory(dir=out) as work:
        for target, executable, suffix in [
            ("windows", "pagetty.exe", ".zip"),
            ("linux", "pagetty", ".tar.gz"),
        ]:
            binary = Path(work) / executable
            env = dict(go_env, GOOS=target, GOARCH="amd64", GOAMD64="v1",
                       CGO_ENABLED="0", GOFLAGS="-mod=readonly", GOEXPERIMENT="")
            subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-o", str(binary),
                            "./cmd/pagetty"], cwd=root, env=env, check=True)
            files = [(executable, binary.read_bytes(), 0o755)]
            files += [(name, (root / name).read_bytes(), 0o644)
                      for name in ["LICENSE", "THIRD_PARTY_NOTICES.txt", "README.md"]]
            if target == "windows":
                installer = (root / "scripts/install.cmd").read_bytes().replace(b"\r\n", b"\n")
                files.append(("install.cmd", installer.replace(b"\n", b"\r\n"), 0o644))
            else:
                files.append(("install.sh", (root / "scripts/install.sh").read_bytes(), 0o755))
            archive = out / ("pagetty-" + target + "-amd64" + suffix)
            if target == "windows":
                with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as z:
                    for name, data, mode in files:
                        info = zipfile.ZipInfo(name, (2000, 1, 1, 0, 0, 0))
                        info.create_system = 3
                        info.external_attr = (0o100000 | mode) << 16
                        info.compress_type = zipfile.ZIP_DEFLATED
                        z.writestr(info, data)
            else:
                with archive.open("wb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", filename="", mtime=0) as gz, tarfile.open(fileobj=gz, mode="w") as tar:
                    for name, data, mode in files:
                        info = tarfile.TarInfo(name)
                        info.size, info.mode = len(data), mode
                        tar.addfile(info, io.BytesIO(data))
            checksums.append(hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name)
    (out / "SHA256SUMS").write_text("\n".join(checksums) + "\n")
    print("\n".join(checksums))


if __name__ == "__main__":
    main()
