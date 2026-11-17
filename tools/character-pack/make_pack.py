#!/usr/bin/env python3
"""把形象包文件夹打包成 zip（manifest.json 放在压缩包根目录）。

用法：
    python tools/character-pack/make_pack.py 我的形象/
    python tools/character-pack/make_pack.py 我的形象/ -o dist/我的形象.zip
"""

import json
import os
import subprocess
import sys
import zipfile

for _s in (sys.stdout, sys.stderr):
    try:
        _s.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass

SKIP_DIRS = {".git", "__MACOSX", ".idea", ".vscode", "node_modules"}
SKIP_FILES = {".DS_Store", "Thumbs.db", "desktop.ini"}


def find_root(folder):
    if os.path.isfile(os.path.join(folder, "manifest.json")):
        return folder
    subs = []
    for name in os.listdir(folder):
        sub = os.path.join(folder, name)
        if os.path.isdir(sub) and os.path.isfile(os.path.join(sub, "manifest.json")):
            subs.append(sub)
    if len(subs) == 1:
        return subs[0]
    if not subs:
        raise SystemExit("[错误] 没有找到 manifest.json")
    raise SystemExit("[错误] 找到多份 manifest.json，无法确定使用哪一份")


def main():
    args = [a for a in sys.argv[1:] if a not in ("-o", "--out")]
    if not args:
        raise SystemExit(__doc__)
    src = os.path.abspath(args[0])
    if not os.path.isdir(src):
        raise SystemExit(f"[错误] 不是文件夹：{src}")
    out = None
    if "-o" in sys.argv or "--out" in sys.argv:
        i = sys.argv.index("-o") if "-o" in sys.argv else sys.argv.index("--out")
        if i + 1 >= len(sys.argv):
            raise SystemExit("[错误] -o 后面缺少输出路径")
        out = os.path.abspath(sys.argv[i + 1])

    validator = os.path.join(os.path.dirname(os.path.abspath(__file__)), "validate.py")
    r = subprocess.run([sys.executable, validator, src])
    if r.returncode != 0:
        raise SystemExit("[错误] 校验未通过，已停止打包")

    root = find_root(src)
    with open(os.path.join(root, "manifest.json"), encoding="utf-8-sig") as f:
        m = json.load(f)
    pack_id = m.get("id") or "character"
    if out is None:
        out = os.path.abspath(src.rstrip("/\\") + ".zip")

    count = 0
    total = 0
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=6) as z:
        for base, dirs, files in os.walk(root):
            dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS)
            for name in sorted(files):
                if name in SKIP_FILES:
                    continue
                full = os.path.join(base, name)
                if os.path.islink(full):
                    raise SystemExit(f"[错误] 不支持符号链接：{full}")
                rel = os.path.relpath(full, root).replace(os.sep, "/")
                z.write(full, rel)
                count += 1
                total += os.path.getsize(full)
    print(f"[完成] {count} 个文件，{total / 1048576:.1f} MB -> {out}")
    print("[提示] 在桌宠 设置 → 桌宠形象 → 从本地文件导入（.zip） 选择这个文件")


if __name__ == "__main__":
    main()
