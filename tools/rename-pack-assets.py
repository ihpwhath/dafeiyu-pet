#!/usr/bin/env python3
"""把形象包的动作文件统一改成 <包id>-<动作名>.webm，方便辨认。

为什么：不同角色包的动作名高度重合（每个包都有「三球抛接」「东张西望」），
混在一个目录里根本分不清是谁的。加前缀后一眼可辨，包内 manifest 的 file
字段同步更新，应用播放时按清单解析，不依赖文件名。

会处理三类位置：
    %APPDATA%\\dafeiyu-desktop-pet\\characters   已安装的
    characters-build                             本地构建产物
    characters-build-pub                         上传用的压缩版

用法：python tools/rename-pack-assets.py
"""

import json
import os
import sys
import zipfile

for _s in (sys.stdout, sys.stderr):
    try:
        _s.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
INSTALL = os.path.join(os.environ["APPDATA"], "dafeiyu-desktop-pet", "characters")
TARGETS = [INSTALL, os.path.join(ROOT, "characters-build"), os.path.join(ROOT, "characters-build-pub")]


def rename_pack(pack_dir):
    mf = os.path.join(pack_dir, "manifest.json")
    if not os.path.isfile(mf):
        return 0
    m = json.loads(open(mf, encoding="utf-8").read())
    pid = m.get("id", "")
    if not pid:
        return 0
    anim = os.path.join(pack_dir, "animations")
    if not os.path.isdir(anim):
        return 0
    n = 0
    for a in m.get("actions", []):
        old_name = os.path.basename(a.get("file", ""))
        if not old_name:
            continue
        # 已经带前缀的跳过
        if old_name.startswith(pid + "-"):
            continue
        old = os.path.join(anim, old_name)
        new = os.path.join(anim, f"{pid}-{old_name}")
        if os.path.isfile(old):
            os.replace(old, new)
            n += 1
        a["file"] = f"animations/{pid}-{old_name}"
    # 清单之外的散文件也顺手补个前缀（中途中断留下的）
    prefix = pid + "-"
    for f in os.listdir(anim):
        if f.endswith(".webm") and not f.startswith(prefix):
            try:
                os.replace(os.path.join(anim, f), os.path.join(anim, prefix + f))
                n += 1
            except OSError:
                pass
    with open(mf, "w", encoding="utf-8") as f:
        json.dump(m, f, ensure_ascii=False, indent=2)
    return n


def rezip(pack_dir):
    """按目录现状重新打包（webm 已压缩过，用 store 模式不耗时）。"""
    zp = pack_dir + ".zip"
    with zipfile.ZipFile(zp, "w", zipfile.ZIP_DEFLATED, compresslevel=6) as z:
        for base, _, files in os.walk(pack_dir):
            for fn in files:
                full = os.path.join(base, fn)
                arc = os.path.relpath(full, pack_dir)
                if fn.endswith((".webm", ".webp")):
                    z.write(full, arc, compress_type=zipfile.ZIP_STORED)
                else:
                    z.write(full, arc)
    return os.path.getsize(zp)


def main():
    for root in TARGETS:
        if not os.path.isdir(root):
            continue
        for name in sorted(os.listdir(root)):
            d = os.path.join(root, name)
            if not os.path.isdir(d) or name.startswith("_"):
                continue
            n = rename_pack(d)
            if n:
                if os.path.isfile(d + ".zip"):
                    size = rezip(d)
                    print(f"{name}: 改名 {n} 个文件，zip 重打 {size // 1048576} MB")
                else:
                    print(f"{name}: 改名 {n} 个文件")


if __name__ == "__main__":
    main()
