#!/usr/bin/env python3
"""把 characters-build/ 里构建好的形象包装进用户目录，供桌宠直接使用。

和 build-character-pack.py 的分工：那个负责「上游精灵图 -> 透明 WebM + manifest」，
这个负责「构建产物 -> %APPDATA%\\dafeiyu-desktop-pet\\characters」。
装了之后应用启动时会自动扫到这个目录，设置在「形象包」里就能看到。

另外会做一件事：目录里已经转好、但 manifest 还没列进去的 webm，按上游 actions.json
补进清单——中途中断过一次构建的话，已完成的动作不会白费。

用法：
    python tools/install-character-packs.py                 # 装 characters-build 下全部
    python tools/install-character-packs.py dsh1 claude     # 只装指定的
"""

import json
import os
import shutil
import sys
import urllib.request

for _s in (sys.stdout, sys.stderr):
    try:
        _s.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BUILD = os.path.join(ROOT, "characters-build")
REPO = "https://raw.githubusercontent.com/hunmer/aboutme-pets/main/"
DEST = os.path.join(os.environ["APPDATA"], "dafeiyu-desktop-pet", "characters")


def slug(pid):
    return pid[:-4] if pid.endswith("-pet") else pid


def main():
    want = set(sys.argv[1:])
    os.makedirs(DEST, exist_ok=True)
    pets = {p["id"]: p for p in json.loads(urllib.request.urlopen(REPO + "pets.json", timeout=30).read())["pets"]}
    by_slug = {slug(k): v for k, v in pets.items()}

    packs = want or sorted(
        d for d in os.listdir(BUILD)
        if os.path.isdir(os.path.join(BUILD, d)) and not d.startswith("_")
        and os.path.isfile(os.path.join(BUILD, d, "manifest.json"))
    )

    total = 0
    for pid in packs:
        src = os.path.join(BUILD, pid)
        mf = os.path.join(src, "manifest.json")
        if not os.path.isfile(mf):
            print(f"{pid}: 跳过（没有 manifest.json）")
            continue
        m = json.loads(open(mf, encoding="utf-8").read())
        known = {a["id"] for a in m["actions"]}
        anim = os.path.join(src, "animations")
        files = sorted(
            f[:-5] for f in os.listdir(anim)
            if f.endswith(".webm") and os.path.getsize(os.path.join(anim, f)) > 1024
        ) if os.path.isdir(anim) else []
        extra = [f for f in files if f not in known]

        if extra and pid in by_slug:
            full = {a["id"]: a for a in json.loads(
                urllib.request.urlopen(REPO + by_slug[pid]["actions"], timeout=90).read())["animations"]}
            added = 0
            for f in extra:
                a = full.get(f)
                if not a:
                    continue
                sp = a["spritesheet"]
                m["actions"].append({
                    "id": f, "category": a.get("category", ""), "file": f"animations/{f}.webm",
                    "frames": sp["frameCount"], "fps": sp["fps"],
                    "durationMs": int(sp["frameCount"] / sp["fps"] * 1000),
                })
                added += 1
            if added:
                print(f"{pid}: 清单补入 {added} 个已转好的动作")

        dst = os.path.join(DEST, pid)
        os.makedirs(dst, exist_ok=True)
        shutil.copytree(src, dst, dirs_exist_ok=True)
        with open(os.path.join(dst, "manifest.json"), "w", encoding="utf-8") as f:
            json.dump(m, f, ensure_ascii=False, indent=2)
        total += len(m["actions"])
        print(f"{pid}: {len(m['actions'])} 个动作 -> {dst}")

    print(f"\n共安装 {len(packs)} 个包 / {total} 个动作 -> {DEST}")


if __name__ == "__main__":
    main()
