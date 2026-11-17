#!/usr/bin/env python3
"""把 aboutme-pets 的角色包转成本应用可用的形象包（透明 WebM + manifest）。

背景：上游 hunmer/aboutme-pets 提供的是透明 WebP 精灵图（640x360，8 列网格，12fps），
本应用的引擎吃的是 VP9 + alpha_mode=1 的透明 WebM，两者用 ffmpeg 直接对转。

产物结构：
    <out>/<packId>/
        manifest.json          # 角色元信息 + 动作表
        cover.webp             # 封面立绘
        animations/<动作>.webm  # 透明动画
    <out>/<packId>.zip         # 上面整个目录打包，供按需下载
    <out>/characters.json      # 所有包的索引（应用侧据此展示下载列表）

用法：
    python tools/build-character-pack.py --pack dsh1
    python tools/build-character-pack.py --all --jobs 6
"""

import argparse
import concurrent.futures
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.parse
import urllib.request
import zipfile

# CI 的 Python stdout 可能是 cp1252，打印中文动作名会直接炸，统一改 UTF-8
for _s in (sys.stdout, sys.stderr):
    try:
        _s.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass

# raw.githubusercontent 在国内经常被限流/502，加一组镜像轮着试
MIRRORS = [
    "https://cdn.jsdelivr.net/gh/hunmer/aboutme-pets@main/",
    "https://ghfast.top/https://raw.githubusercontent.com/hunmer/aboutme-pets/main/",
    "https://raw.githubusercontent.com/hunmer/aboutme-pets/main/",
]
REPO = MIRRORS[0]
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_OUT = os.path.join(ROOT, "characters-build")
CACHE = os.path.join(DEFAULT_OUT, "_cache")


def fetch(url, dest, retries=2):
    """带缓存的下载：已存在且非空就跳过。失败时自动换镜像重试。"""
    if os.path.isfile(dest) and os.path.getsize(dest) > 0:
        return dest
    os.makedirs(os.path.dirname(dest), exist_ok=True)

    # url 里含 REPO 前缀时，逐个镜像替换后再试
    candidates = []
    tail = None
    for m in MIRRORS:
        if url.startswith(m):
            tail = url[len(m):]
            break
    if tail is None and url.startswith(REPO):
        tail = url[len(REPO):]
    candidates = [m + tail for m in MIRRORS] if tail else [url]

    last = None
    for i in range(retries):
        for cand in candidates:
            try:
                tmp = dest + ".part"
                with urllib.request.urlopen(cand, timeout=20) as r, open(tmp, "wb") as f:
                    shutil.copyfileobj(r, f)
                os.replace(tmp, dest)
                return dest
            except Exception as e:  # 换下一个镜像
                last = e
        time.sleep(1.0 * (i + 1))
    raise RuntimeError(f"下载失败 {url}: {last}")


def fetch_json(rel):
    path = os.path.join(CACHE, rel)
    fetch(REPO + rel, path)
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def load_pets():
    return fetch_json("pets.json")["pets"]


def human(n):
    return f"{n/1048576:.1f} MB" if n >= 1048576 else f"{n/1024:.0f} KB"


def convert_one(job, ffmpeg, crf, cpu_used=4, threads=0, low_prio=False, purge=False, sharpness=0):
    """精灵图 -> 透明 WebM。alpha 必须显式开 alphamerge：libvpx 需要 -alpha_mode 1。"""
    sheet, out, meta = job
    if os.path.isfile(out) and os.path.getsize(out) > 0:
        return out, os.path.getsize(out), True
    sp = meta["spritesheet"]
    fw, fh, cols = sp["frameWidth"], sp["frameHeight"], sp["columns"]
    frames, fps = sp["frameCount"], sp["fps"]
    vf = (f"crop={fw}:{fh}:x='mod(n\\,{cols})*{fw}':y='floor(n/{cols})*{fh}',"
          f"format=yuva420p")
    cmd = [
        ffmpeg, "-hide_banner", "-nostdin", "-y", "-v", "error",
        "-loop", "1", "-framerate", str(fps), "-i", sheet,
        "-frames:v", str(frames),
        "-vf", vf, "-fps_mode", "passthrough",
        "-c:v", "libvpx-vp9", "-auto-alt-ref", "0",
        "-pix_fmt", "yuva420p", "-row-mt", "1",
        # VP9 默认是慢速"best quality"档：120 帧要十几秒，167 个动作要一个多小时。
        # good + cpu-used 2 在画质/耗时之间平衡；需要更快可显式调大 --cpu-used。
        "-deadline", "good", "-cpu-used", str(cpu_used),
        "-b:v", "0", "-crf", str(crf),
        "-alpha_mode", "1", "-metadata:s:v:0", "alpha_mode=1",
    ]
    if sharpness:
        cmd += ["-sharpness", str(sharpness)]
    cmd.append(out)
    if threads:
        cmd += ["-threads", str(threads)]
    flags = 0x4000 if low_prio else 0   # BELOW_NORMAL_PRIORITY_CLASS，机器不卡
    r = subprocess.run(cmd, capture_output=True, text=True, creationflags=flags)
    if purge:
        try:
            os.remove(sheet)
        except OSError:
            pass
    if r.returncode != 0 or not os.path.isfile(out):
        raise RuntimeError(f"{os.path.basename(out)} 转换失败: {r.stderr.strip()[:300]}")
    return out, os.path.getsize(out), False


def slug_of(pack):
    """上游 id 形如 dsh1-pet，本地目录统一用短名 dsh1。"""
    pid = pack["id"]
    return pid[:-4] if pid.endswith("-pet") else pid


def select_subset(actions, limit):
    """挑前 limit 个动作，但保证每个分类至少有一条——小样包也要能覆盖各个行为槽位。"""
    if not limit or limit >= len(actions):
        return actions
    picked, seen_cat = [], set()
    for a in actions:                      # 第一轮：每类取一条
        c = a.get("category", "")
        if c not in seen_cat:
            seen_cat.add(c)
            picked.append(a)
            if len(picked) >= limit:
                return picked
    for a in actions:                      # 第二轮：按原顺序补满
        if a not in picked:
            picked.append(a)
            if len(picked) >= limit:
                break
    return picked


def convert_one_pil(job, ffmpeg, crf, cpu_used=6, threads=1, low_prio=True, purge=False, sharpness=0):
    """PIL 逐帧裁 + 裸像素管道喂 ffmpeg。

    源精灵图是 5120x8280 的大图，用 ffmpeg 的 crop 滤镜按 n 逐帧裁时，
    它要把整张图解码进内存并做多层缓冲，实测单进程 830MB，8 个并发直接把机器压死。
    这里改成 PIL 裁一帧、写一帧到管道，内存基本只跟单帧（0.9MB）和编码器有关。
    """
    sheet, out, meta = job
    if os.path.isfile(out) and os.path.getsize(out) > 0:
        return out, os.path.getsize(out), True

    sp = meta["spritesheet"]
    fw, fh, cols = sp["frameWidth"], sp["frameHeight"], sp["columns"]
    frames, fps = sp["frameCount"], sp["fps"]

    from PIL import Image
    im = Image.open(sheet).convert("RGBA")

    cmd = [
        ffmpeg, "-hide_banner", "-nostdin", "-y", "-v", "error",
        "-f", "rawvideo", "-pix_fmt", "rgba", "-s", f"{fw}x{fh}", "-r", str(fps), "-i", "-",
        "-c:v", "libvpx-vp9", "-auto-alt-ref", "0",
        "-pix_fmt", "yuva420p", "-row-mt", "1",
        "-deadline", "good", "-cpu-used", str(cpu_used),
        "-b:v", "0", "-crf", str(crf),
        "-alpha_mode", "1", "-metadata:s:v:0", "alpha_mode=1",
    ]
    if sharpness:
        cmd += ["-sharpness", str(sharpness)]
    if threads:
        cmd += ["-threads", str(threads), "-filter_threads", "1"]
    cmd.append(out)

    flags = 0x4000 if low_prio else 0
    proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stderr=subprocess.PIPE, creationflags=flags)
    try:
        for i in range(frames):
            col, row = i % cols, i // cols
            box = (col * fw, row * fh, col * fw + fw, row * fh + fh)
            proc.stdin.write(im.crop(box).tobytes())
        proc.stdin.close()
    except Exception:
        proc.kill()
        raise
    err = proc.stderr.read().decode("utf-8", "replace")
    rc = proc.wait()
    im.close()
    if purge:
        try:
            os.remove(sheet)
        except OSError:
            pass
    if rc != 0 or not os.path.isfile(out):
        raise RuntimeError(f"{os.path.basename(out)} 转换失败: {err.strip()[:300]}")
    return out, os.path.getsize(out), False


def build_pack(pack, out_root, crf, jobs, ffmpeg, limit=0, cpu_used=4, threads=0, low_prio=False, purge=False, encoder="pil", sharpness=0):
    pid = slug_of(pack)
    pack_dir = os.path.join(out_root, pid)
    anim_dir = os.path.join(pack_dir, "animations")
    os.makedirs(anim_dir, exist_ok=True)

    actions = fetch_json(pack["actions"])["animations"]
    total_n = len(actions)
    actions = select_subset(actions, limit)
    print(f"[{pid}] {pack['name']}：{len(actions)}/{total_n} 个动作")

    # 封面
    cover_src = fetch(REPO + urllib.parse.quote(pack["cover"]),
                      os.path.join(CACHE, pack["cover"].replace("/", "_")))
    shutil.copyfile(cover_src, os.path.join(pack_dir, "cover.webp"))

    pack_base = pack["actions"].rsplit("/", 1)[0]  # pets/dsh1
    todo = []
    for a in actions:
        rel = a["sources"]["webp"]                      # spritesheet/中国功夫.webp
        url = f"{REPO}{pack_base}/{urllib.parse.quote(rel)}"
        sheet = os.path.join(CACHE, pid + "_sheet", rel.replace("/", "_"))
        todo.append((a, url, sheet, os.path.join(anim_dir, a["id"] + ".webm")))

    # 并发下载（IO 密集，单独一轮，避免和 ffmpeg 抢 CPU）
    print(f"[{pid}] 下载精灵图…")
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as ex:
        list(ex.map(lambda t: fetch(t[1], t[2]), todo))

    print(f"[{pid}] 转码（{jobs} 并发）…")
    total = 0
    done = 0
    t0 = time.time()
    with concurrent.futures.ThreadPoolExecutor(max_workers=jobs) as ex:
        fn = convert_one_pil if encoder == "pil" else convert_one
        futs = [ex.submit(fn, (t[2], t[3], t[0]), ffmpeg, crf, cpu_used, threads, low_prio, purge, sharpness) for t in todo]
        for f in concurrent.futures.as_completed(futs):
            _, size, cached = f.result()
            total += size
            done += 1
            if done % 20 == 0 or done == len(futs):
                print(f"   {done}/{len(futs)}  {human(total)}  {time.time()-t0:.0f}s")

    # manifest
    manifest = {
        "schemaVersion": 1,
        "id": pid,
        "sourcePackId": pack["id"],
        "name": pack["name"],
        "description": pack.get("description", ""),
        "cover": "cover.webp",
        "source": "https://github.com/hunmer/aboutme-pets",
        "license": "CC BY-NC-SA 4.0（立绘来自 lokicorvus/crosspet）",
        "actions": [
            {
                "id": a["id"],
                "category": a.get("category", ""),
                "file": f"animations/{a['id']}.webm",
                "frames": a["spritesheet"]["frameCount"],
                "fps": a["spritesheet"]["fps"],
                "durationMs": int(a["spritesheet"]["frameCount"] / a["spritesheet"]["fps"] * 1000),
                "hasAudio": bool(a.get("audio")),
            }
            for a in actions
        ],
    }
    with open(os.path.join(pack_dir, "manifest.json"), "w", encoding="utf-8") as f:
        json.dump(manifest, f, ensure_ascii=False, indent=2)

    # 打包（webm 已压缩过，直接 store，避免几百 MB 的无效 deflate）
    zip_path = os.path.join(out_root, pid + ".zip")
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED, compresslevel=6) as z:
        for base, _, files in os.walk(pack_dir):
            for fn in files:
                full = os.path.join(base, fn)
                arc = os.path.relpath(full, pack_dir)
                if fn.endswith(".webm") or fn.endswith(".webp"):
                    z.write(full, arc, compress_type=zipfile.ZIP_STORED)
                else:
                    z.write(full, arc)
    print(f"[{pid}] 完成：{len(actions)} 动作 / 动画合计 {human(total)} / 包 {human(os.path.getsize(zip_path))}")
    return {
        "id": pid,
        "name": pack["name"],
        "description": pack.get("description", ""),
        "actions": len(actions),
        "zip": pid + ".zip",
        "zipBytes": os.path.getsize(zip_path),
        "license": manifest["license"],
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pack", action="append", help="包 id（可多次指定），默认 dsh1")
    ap.add_argument("--all", action="store_true", help="构建全部包")
    ap.add_argument("--out", default=DEFAULT_OUT)
    ap.add_argument("--crf", type=int, default=32)
    ap.add_argument("--jobs", type=int, default=6)
    ap.add_argument("--ffmpeg", default="ffmpeg")
    ap.add_argument("--limit", type=int, default=0, help="每个角色只做前 N 个动作（0=全部），用于快速出小样")
    ap.add_argument("--skip", action="append", default=[], help="跳过的包 id")
    ap.add_argument("--cpu-used", type=int, default=2, help="VP9 速度档 0-8，越大越快")
    ap.add_argument("--sharpness", type=int, default=2, help="VP9 锐度 0-7；默认 2，补偿去块滤波的糊感")
    ap.add_argument("--install", action="store_true", help="构建后直接装进用户目录")
    ap.add_argument("--threads", type=int, default=0, help="每个 ffmpeg 的线程数（0=不限），配合 --jobs 控制总负载")
    ap.add_argument("--low-priority", action="store_true", help="以低优先级运行 ffmpeg，避免卡住桌面")
    ap.add_argument("--purge", action="store_true", help="转完就删掉精灵图缓存，省硬盘")
    ap.add_argument("--encoder", choices=["pil", "ffmpeg"], default="pil",
                    help="pil=PIL 逐帧裁+管道喂（省内存，推荐）；ffmpeg=滤镜裁切（快但单进程约 800MB）")
    args = ap.parse_args()

    os.makedirs(args.out, exist_ok=True)
    pets = load_pets()
    if args.all:
        targets = pets
    else:
        want = set(args.pack or ["dsh1"])
        by_key = {}
        for p in pets:
            by_key[p["id"]] = p
            by_key[slug_of(p)] = p
        unknown = want - set(by_key)
        if unknown:
            sys.exit(f"未知包 id: {', '.join(sorted(unknown))}（可用：{', '.join(by_key)}）")
        targets = [by_key[k] for k in (args.pack or ["dsh1"])]
    skip = {x for x in (args.skip or [])}
    targets = [p for p in targets if p["id"] not in skip and slug_of(p) not in skip]

    index = []
    for p in targets:
        index.append(build_pack(p, args.out, args.crf, args.jobs, args.ffmpeg, args.limit, args.cpu_used, args.threads, args.low_priority, args.purge, args.encoder, args.sharpness))

    idx_path = os.path.join(args.out, "characters.json")
    with open(idx_path, "w", encoding="utf-8") as f:
        json.dump({"schemaVersion": 1, "generatedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ"), "packs": index},
                  f, ensure_ascii=False, indent=2)
    print(f"\n索引已写出：{idx_path}")


if __name__ == "__main__":
    main()
