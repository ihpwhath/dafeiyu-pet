#!/usr/bin/env python3
"""把已发布的形象包转成 Lite 版：480×270 透明 VP9，去掉音轨。

输入：characters-build/full/<id>.zip（素材仓库的完整包）
输出：characters-build/lite/<id>.zip + characters-build/lite/characters.json
用法：python tools/build-character-pack-lite.py [--only gemini] [--limit 3] [--jobs 3]
"""

import argparse
import concurrent.futures
import json
import os
import shutil
import subprocess
import sys
import tempfile
import zipfile

for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_IN = os.path.join(ROOT, "characters-build", "full")
DEFAULT_OUT = os.path.join(ROOT, "characters-build", "lite")
CATALOG_IN = os.path.join(ROOT, "characters-build", "characters.json")
PACK_RAW = "https://github.com/shenliuyu18/dafeiyu-pet-assets/releases/download/packs-2026-11/"

WIDTH, HEIGHT = 480, 270


def human(n):
    return f"{n / 1048576:.1f} MB" if n >= 1048576 else f"{n / 1024:.0f} KB"


def probe(path):
    r = subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration",
         "-show_entries", "stream=width,height", "-show_entries", "stream_tags=ALPHA_MODE",
         "-of", "default=noprint_wrappers=1", path],
        capture_output=True,
    )
    text = r.stdout.decode("utf-8", "ignore")
    dur, w, h, alpha = 0.0, 0, 0, False
    for line in text.splitlines():
        if line.startswith("duration="):
            try:
                dur = float(line.split("=", 1)[1])
            except ValueError:
                pass
        elif line.startswith("width="):
            w = int(line.split("=", 1)[1] or 0)
        elif line.startswith("height="):
            h = int(line.split("=", 1)[1] or 0)
        elif "ALPHA_MODE=1" in line:
            alpha = True
    return dur, w, h, alpha


def transcode(path, crf, cpu_used):
    """原地换成 480×270；任何异常都保留原文件，绝不让包缺动作。"""
    src_dur, _, _, _ = probe(path)
    fd, tmp = tempfile.mkstemp(suffix=".webm", dir=os.path.dirname(path))
    os.close(fd)
    try:
        r = subprocess.run([
            "ffmpeg", "-hide_banner", "-nostdin", "-y", "-v", "error",
            "-c:v", "libvpx-vp9", "-i", path,
            "-vf", f"scale={WIDTH}:{HEIGHT}:flags=lanczos",
            "-c:v", "libvpx-vp9", "-pix_fmt", "yuva420p", "-crf", str(crf), "-b:v", "0",
            "-row-mt", "1", "-lag-in-frames", "25", "-auto-alt-ref", "1",
            "-deadline", "good", "-cpu-used", str(cpu_used), "-threads", "1",
            "-an", tmp,
        ], capture_output=True)
        if r.returncode != 0:
            raise RuntimeError(r.stderr.decode("utf-8", "ignore")[:300])
        dur, w, h, alpha = probe(tmp)
        if not alpha or w != WIDTH or h != HEIGHT or abs(dur - src_dur) > 0.2:
            raise RuntimeError(f"verify failed (alpha={alpha}, {w}x{h}, {dur:.2f}s vs {src_dur:.2f}s)")
        if os.path.getsize(tmp) >= os.path.getsize(path):
            return "copy", os.path.getsize(path)
        os.replace(tmp, path)
        return "ok", os.path.getsize(path)
    except Exception as e:
        return "fallback:" + str(e)[:80], os.path.getsize(path)
    finally:
        if os.path.exists(tmp):
            os.remove(tmp)


def build_pack(zip_path, out_dir, jobs, crf, cpu_used, limit):
    pid = os.path.splitext(os.path.basename(zip_path))[0]
    out_zip = os.path.join(out_dir, pid + ".zip")
    if os.path.exists(out_zip) and os.path.getmtime(out_zip) >= os.path.getmtime(zip_path):
        print(f"[{pid}] 已是最新：{human(os.path.getsize(out_zip))}")
        return out_zip

    tmp_dir = tempfile.mkdtemp(prefix=f"packlite-{pid}-")
    try:
        with zipfile.ZipFile(zip_path) as z:
            z.extractall(tmp_dir)
            names = z.namelist()
        anims = sorted(
            n for n in names
            if n.startswith("animations/") and n.endswith(".webm")
        )
        if limit:
            anims = anims[:limit]
        print(f"[{pid}] 动画 {len(anims)} 个，转码 {WIDTH}×{HEIGHT} CRF {crf}…")

        total = 0
        stats = {}
        done = 0

        def one(rel):
            return rel, transcode(os.path.join(tmp_dir, rel), crf, cpu_used)

        if anims:
            with concurrent.futures.ThreadPoolExecutor(max_workers=jobs) as ex:
                futs = [ex.submit(one, rel) for rel in anims]
                for f in concurrent.futures.as_completed(futs):
                    rel, (kind, size) = f.result()
                    total += size
                    stats[kind] = stats.get(kind, 0) + 1
                    done += 1
                    if done % 20 == 0 or done == len(futs):
                        print(f"   {done}/{len(futs)}  {human(total)}  {stats}")

        fd, tmp_zip = tempfile.mkstemp(suffix=".zip", dir=out_dir)
        os.close(fd)
        with zipfile.ZipFile(tmp_zip, "w", zipfile.ZIP_DEFLATED, compresslevel=6) as z:
            for base, _, files in os.walk(tmp_dir):
                for fn in files:
                    full = os.path.join(base, fn)
                    arc = os.path.relpath(full, tmp_dir).replace("\\", "/")
                    if fn.endswith((".webm", ".webp")):
                        z.write(full, arc, compress_type=zipfile.ZIP_STORED)
                    else:
                        z.write(full, arc)
        os.replace(tmp_zip, out_zip)
        print(f"[{pid}] 完成：{human(os.path.getsize(zip_path))} → {human(os.path.getsize(out_zip))}")
        return out_zip
    finally:
        shutil.rmtree(tmp_dir, ignore_errors=True)


def write_catalog(out_dir, zips):
    if not os.path.exists(CATALOG_IN):
        print(f"缺少 catalog：{CATALOG_IN}")
        return
    with open(CATALOG_IN, encoding="utf-8") as f:
        doc = json.load(f)
    for p in doc.get("packs", []):
        zp = zips.get(p.get("id"))
        if not zp:
            continue
        name = os.path.basename(zp)
        asset = ("gptv2" if p.get("id") == "gpt" else p.get("id")) + "-lite.zip"
        p["liteZip"] = asset
        p["liteZipBytes"] = os.path.getsize(zp)
        p["liteUrl"] = PACK_RAW + asset
        # 顺手校正完整包的真实大小和动作数（旧 catalog 里有明显过期的值）
        full = os.path.join(DEFAULT_IN, name)
        if os.path.exists(full):
            p["zipBytes"] = os.path.getsize(full)
            try:
                with zipfile.ZipFile(full) as z:
                    doc_in = json.loads(z.read("manifest.json").decode("utf-8"))
                p["actions"] = len(doc_in.get("actions", [])) or p.get("actions", 0)
            except Exception:
                pass
    path = os.path.join(out_dir, "characters.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(doc, f, ensure_ascii=False, indent=2)
    print(f"catalog: {path}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--in", dest="in_dir", default=DEFAULT_IN)
    ap.add_argument("--out", default=DEFAULT_OUT)
    ap.add_argument("--only", default="", help="只构建这些 id，逗号分隔")
    ap.add_argument("--jobs", type=int, default=3)
    ap.add_argument("--crf", type=int, default=42)
    ap.add_argument("--cpu-used", type=int, default=4)
    ap.add_argument("--limit", type=int, default=0, help="每个包只转前 N 个动作，用于试跑")
    args = ap.parse_args()

    os.makedirs(args.out, exist_ok=True)
    only = {x.strip() for x in args.only.split(",") if x.strip()}
    zips = {}
    for fn in sorted(os.listdir(args.in_dir)):
        if not fn.endswith(".zip"):
            continue
        pid = os.path.splitext(fn)[0]
        if only and pid not in only:
            continue
        try:
            zips[pid] = build_pack(
                os.path.join(args.in_dir, fn), args.out, args.jobs, args.crf, args.cpu_used, args.limit
            )
        except Exception as e:
            print(f"[{pid}] 失败：{e}")
    if not only or args.limit == 0:
        write_catalog(args.out, zips)
    print(f"Lite 包总大小：{human(sum(os.path.getsize(p) for p in zips.values()))}")


if __name__ == "__main__":
    main()
