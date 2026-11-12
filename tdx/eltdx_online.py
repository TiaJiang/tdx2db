"""Fetch TDX online metadata through the eltdx Python library."""

import hashlib
import json
import sys
from pathlib import Path

try:
    from eltdx import TdxClient
    from eltdx.hosts import DEFAULT_HOSTS
except ImportError:
    print(
        'eltdx is required: python3 -m pip install "eltdx>=3.3.5" '
        '(Windows: python -m pip install "eltdx>=3.3.5")',
        file=sys.stderr,
    )
    raise SystemExit(1)


MARKET_PREFIX = {0: "sz", 1: "sh", 2: "bj"}
TDX_TIMEOUT = 10
TDX_HOST_ATTEMPTS = 8
BOARD_CACHE_FILES = ("tdxhy.cfg",)
CODE_PAGE_SIZE = 1600


def market_symbol(market, code):
    code = str(code or "").strip().lower()
    if code.startswith(("sh", "sz", "bj")):
        return code
    try:
        prefix = MARKET_PREFIX.get(int(market))
    except (TypeError, ValueError):
        prefix = None
    return f"{prefix}{code}" if prefix and len(code) == 6 else ""


def fetch_symbols():
    rows = None
    last_error = None
    for host in DEFAULT_HOSTS[:TDX_HOST_ATTEMPTS]:
        try:
            with TdxClient(
                hosts=[host],
                timeout=TDX_TIMEOUT,
                probe_hosts=False,
                server_count=1,
                pool_size=1,
                connections_per_server=1,
            ) as client:
                rows = []
                for market in ("sh", "sz", "bj"):
                    start = 0
                    while True:
                        page = client.codes.list(
                            market,
                            start=start,
                            limit=CODE_PAGE_SIZE,
                        )
                        if not page:
                            break
                        rows.extend(page)
                        if len(page) < CODE_PAGE_SIZE:
                            break
                        start += len(page)
            break
        except Exception as exc:
            last_error = exc
    if rows is None:
        raise last_error or RuntimeError("no TDX code-list host available")

    symbols = []
    for row in rows:
        symbol = str(getattr(row, "full_code", "") or "").strip()
        name = str(getattr(row, "name", "") or "").strip()
        if symbol and name:
            symbols.append({"symbol": symbol, "name": name})
    return {"symbols": symbols}


def _file_md5(path):
    digest = hashlib.md5()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def sync_blocks(tasks_path):
    with open(tasks_path, "r", encoding="utf-8") as handle:
        task = json.load(handle)

    raw_cache_dir = str(task.get("cache_dir") or "").strip()
    if not raw_cache_dir:
        raise ValueError("cache_dir is required")
    cache_dir = Path(raw_cache_dir).expanduser()
    cache_dir.mkdir(parents=True, exist_ok=True)

    requested = task.get("files") or BOARD_CACHE_FILES
    names = [name for name in requested if name in BOARD_CACHE_FILES]
    states = {}
    downloads = []
    warnings = []
    meta_requests = 0
    download_bytes = 0

    client = TdxClient(
        hosts=DEFAULT_HOSTS[:TDX_HOST_ATTEMPTS],
        timeout=TDX_TIMEOUT,
        probe_hosts=False,
        server_count=1,
        pool_size=1,
        connections_per_server=1,
    )
    with client:
        # Always query all metadata first, then download changed files serially.
        for name in names:
            target = cache_dir / name
            meta_requests += 1
            remote = None
            try:
                remote = client.resources.block_meta(name)
            except Exception as exc:
                warning = f"{name} block_meta failed: {type(exc).__name__}: {exc}"
                warnings.append(warning)

            remote_size = int(getattr(remote, "size", 0) or 0)
            remote_md5 = str(getattr(remote, "md5", "") or "").strip().lower()
            local_md5 = _file_md5(target) if target.is_file() else ""

            if remote and target.is_file() and local_md5 == remote_md5 and target.stat().st_size == remote_size:
                states[name] = {
                    "name": name,
                    "status": "unchanged",
                    "cache_path": str(target),
                    "md5": remote_md5,
                    "size": remote_size,
                }
            elif remote:
                downloads.append((name, target, remote_size, remote_md5))
            elif target.is_file():
                states[name] = {
                    "name": name,
                    "status": "cached",
                    "cache_path": str(target),
                    "md5": local_md5,
                    "size": target.stat().st_size,
                }
            else:
                states[name] = {
                    "name": name,
                    "status": "failed",
                    "cache_path": str(target),
                    "error": "block_meta unavailable and no cached file",
                }

        for name, target, remote_size, remote_md5 in downloads:
            try:
                payload = client.resources.download_file(name)
                actual_md5 = hashlib.md5(payload).hexdigest()
                if len(payload) != remote_size or actual_md5 != remote_md5:
                    raise ValueError(
                        f"downloaded file validation failed: size={len(payload)}, "
                        f"md5={actual_md5}"
                    )
                staging = target.with_name(f"{name}.download")
                staging.write_bytes(payload)
                download_bytes += len(payload)
                states[name] = {
                    "name": name,
                    "status": "downloaded",
                    "cache_path": str(target),
                    "staging_path": str(staging),
                    "md5": actual_md5,
                    "size": len(payload),
                }
            except Exception as exc:
                warnings.append(
                    f"{name} download_file failed: {type(exc).__name__}: {exc}"
                )
                if target.is_file():
                    states[name] = {
                        "name": name,
                        "status": "cached",
                        "cache_path": str(target),
                        "md5": _file_md5(target),
                        "size": target.stat().st_size,
                    }
                else:
                    states[name] = {
                        "name": name,
                        "status": "failed",
                        "cache_path": str(target),
                        "error": str(exc),
                    }

    return {
        "files": [states[name] for name in names if name in states],
        "warnings": sorted(set(warnings)),
        "meta_requests": meta_requests,
        "file_downloads": len(downloads),
        "download_bytes": download_bytes,
    }


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else ""
    if mode == "symbols":
        payload = fetch_symbols()
    elif mode == "sync-blocks":
        if len(sys.argv) < 3:
            raise ValueError("sync-blocks mode requires a task JSON path")
        payload = sync_blocks(sys.argv[2])
    else:
        raise ValueError("mode must be symbols or sync-blocks")
    json.dump(payload, sys.stdout, ensure_ascii=False, separators=(",", ":"))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(f"{type(exc).__name__}: {exc}", file=sys.stderr)
        raise SystemExit(1)
