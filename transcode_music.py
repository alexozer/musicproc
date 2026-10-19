#!/usr/bin/env python3
"""
Mirror ~/Music into ~/Downloads/music-transcoded, transcoding lossless audio
(FLAC, WAV, AIFF, ALAC, ...) to high-quality Opus (192k VBR) and copying
every other file over as-is. FLACs go through opusenc, which preserves tags
and embedded album art automatically. Re-runs are incremental: up-to-date
outputs are skipped. Outputs whose source file no longer exists are pruned,
so the destination stays an exact mirror of ~/Music.

Usage:
    python3 transcode_music.py [--dry-run] [--workers N] [--force] [--no-prune]
"""

import argparse
import os
import shutil
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

SOURCE = Path.home() / "Music"
DEST = Path.home() / "Downloads" / "music-transcoded"

# Always lossless containers/formats -> transcode
LOSSLESS_EXTS = {".flac", ".wav", ".aiff", ".aif", ".aifc", ".alac", ".ape", ".wv", ".tta"}
# Containers that may hold either ALAC (lossless) or lossy audio -> probe with ffprobe
AMBIGUOUS_EXTS = {".m4a", ".m4b", ".m4p", ".mp4", ".caf"}

OPUS_BITRATE = "192k"

# Syncthing markers living in the destination folder; never prune these
SYNCTHING_MARKERS = {".stfolder", ".stignore", ".stversions"}


def is_hidden(path: Path, root: Path) -> bool:
    return any(part.startswith(".") for part in path.relative_to(root).parts)


def probe_audio_codec(path: Path) -> str:
    try:
        r = subprocess.run(
            ["ffprobe", "-v", "error", "-select_streams", "a:0",
             "-show_entries", "stream=codec_name", "-of", "csv=p=0", str(path)],
            capture_output=True, text=True, timeout=30,
        )
        return r.stdout.strip().splitlines()[0].strip() if r.returncode == 0 else ""
    except Exception:
        return ""


def classify(path: Path) -> str:
    ext = path.suffix.lower()
    if ext in LOSSLESS_EXTS:
        return "transcode"
    if ext in AMBIGUOUS_EXTS:
        return "transcode" if probe_audio_codec(path) == "alac" else "copy"
    return "copy"


def transcode(src: Path, dst: Path) -> None:
    dst.parent.mkdir(parents=True, exist_ok=True)
    if src.suffix.lower() == ".flac":
        # opusenc reads FLAC natively; tags and embedded art carry over automatically
        tmp = dst.with_name(dst.stem + ".part.opus")  # opusenc requires a .opus output name
        cmd = ["opusenc", "--quiet", "--bitrate", OPUS_BITRATE, str(src), str(tmp)]
    else:
        tmp = dst.with_name(dst.name + ".part")
        cmd = [
            "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
            "-i", str(src),
            "-map", "0:a", "-map_metadata", "0", "-vn",
            "-c:a", "libopus", "-b:a", OPUS_BITRATE, "-vbr", "on",
            "-f", "opus",  # temp file doesn't end in .opus, so tell ffmpeg the container explicitly
            str(tmp),
        ]
    r = subprocess.run(cmd, capture_output=True, text=True)
    if r.returncode != 0:
        tmp.unlink(missing_ok=True)
        err = r.stderr.strip().splitlines()[-1] if r.stderr.strip() else f"{cmd[0]} exited {r.returncode}"
        raise RuntimeError(err)
    tmp.replace(dst)


def copy_file(src: Path, dst: Path) -> None:
    dst.parent.mkdir(parents=True, exist_ok=True)
    tmp = dst.with_name(dst.name + ".part")
    shutil.copy2(src, tmp)
    tmp.replace(dst)


def run_job(action: str, src: Path, dst: Path) -> None:
    (transcode if action == "transcode" else copy_file)(src, dst)


def is_syncthing_marker(path: Path) -> bool:
    return any(part in SYNCTHING_MARKERS for part in path.relative_to(DEST).parts)


def prune_stale(expected: set[str], dry_run: bool) -> int:
    """Delete files in DEST with no corresponding source file, then remove
    directories left empty. Returns the number of files that are stale."""
    if not DEST.is_dir():
        return 0
    stale = sorted(
        f for f in DEST.rglob("*")
        if f.is_file()
        and f.relative_to(DEST).as_posix() not in expected
        and not is_syncthing_marker(f)
    )
    for f in stale:
        if dry_run:
            print(f"  [DEL ] {f.relative_to(DEST)}")
        else:
            f.unlink()
    if not dry_run:
        for d in sorted((p for p in DEST.rglob("*") if p.is_dir() and not is_syncthing_marker(p)),
                        key=lambda p: len(p.parts), reverse=True):
            try:
                d.rmdir()  # only succeeds when empty
            except OSError:
                pass
    return len(stale)


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--dry-run", action="store_true", help="show what would be done without doing it")
    ap.add_argument("--workers", type=int, default=os.cpu_count() or 4,
                    help=f"parallel ffmpeg jobs (default: CPU count, {os.cpu_count() or 4})")
    ap.add_argument("--force", action="store_true", help="re-process even if output already exists and is up to date")
    ap.add_argument("--no-prune", action="store_true", help="do not delete outputs whose source is gone")
    args = ap.parse_args()

    if shutil.which("ffmpeg") is None or shutil.which("ffprobe") is None or shutil.which("opusenc") is None:
        sys.exit("error: ffmpeg/ffprobe/opusenc not found in PATH (brew install ffmpeg opus-tools)")

    if not SOURCE.is_dir():
        sys.exit(f"error: source directory not found: {SOURCE}")

    entries = []
    for src in sorted(SOURCE.rglob("*")):
        if not src.is_file() or is_hidden(src, SOURCE):
            continue
        rel = src.relative_to(SOURCE)
        action = classify(src)
        dst = DEST / (rel.with_suffix(".opus") if action == "transcode" else rel)
        entries.append((action, src, dst))

    if not args.no_prune:
        if not entries:
            print("warning: no source files found; skipping prune for safety", file=sys.stderr)
        else:
            expected = {dst.relative_to(DEST).as_posix() for _, _, dst in entries}
            n_stale = prune_stale(expected, args.dry_run)
            if n_stale and not args.dry_run:
                print(f"pruned {n_stale} stale file(s) from destination")
            elif n_stale > 25 and args.dry_run:
                print(f"  ... and {n_stale - 25} more would be deleted")

    jobs = [
        (a, s, d) for a, s, d in entries
        if args.force or not (d.exists() and d.stat().st_mtime >= s.stat().st_mtime)
    ]

    n_transcode = sum(1 for a, *_ in jobs if a == "transcode")
    n_copy = len(jobs) - n_transcode
    print(f"{n_transcode} file(s) to transcode -> Opus {OPUS_BITRATE}, {n_copy} file(s) to copy")
    print(f"destination: {DEST}")

    if args.dry_run:
        for action, src, _ in jobs[:25]:
            print(f"  [{'OPUS' if action == 'transcode' else 'COPY'}] {src.relative_to(SOURCE)}")
        if len(jobs) > 25:
            print(f"  ... and {len(jobs) - 25} more")
        return

    if not jobs:
        print("nothing to do")
        return

    failures: list[tuple[Path, Exception]] = []
    done = 0
    with ThreadPoolExecutor(max_workers=args.workers) as ex:
        futs = {ex.submit(run_job, a, s, d): (a, s) for a, s, d in jobs}
        for fut in as_completed(futs):
            action, src = futs[fut]
            done += 1
            try:
                fut.result()
            except Exception as e:
                failures.append((src, e))
                print(f"\nFAIL [{action}] {src}: {e}", file=sys.stderr)
            print(f"\r{done}/{len(jobs)} done", end="", flush=True, file=sys.stderr)
    print(file=sys.stderr)

    if failures:
        print(f"{len(failures)} job(s) failed:", file=sys.stderr)
        for src, e in failures:
            print(f"  {src}: {e}", file=sys.stderr)
        sys.exit(1)
    print("all done")


if __name__ == "__main__":
    main()
