"""Fetch TDX online metadata through the eltdx Python library."""

import hashlib
import json
import sys
from pathlib import Path
import time

try:
    from eltdx import F10Client, TdxClient
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
F10_TIMEOUT = 10
F10_ATTEMPTS = 3
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


def call_with_retry(operation, *args, **kwargs):
    last_error = None
    for attempt in range(1, F10_ATTEMPTS + 1):
        try:
            return operation(*args, **kwargs)
        except Exception as exc:
            last_error = exc
            if attempt < F10_ATTEMPTS:
                time.sleep(0.2 * attempt)
    raise last_error


def theme_rows(client, block_code):
    response = call_with_retry(
        client.theme_market,
        block_code,
        200744,
        page=-1,
        page_size=-1,
    )
    if not response.ok or len(response.result_sets) < 2:
        return []
    return response.result_sets[1].rows


def fetch_theme_task(client, task):
    block_code = str(task.get("block_code") or "").strip()
    block_type = str(task.get("block_type") or "").strip()
    if not block_code or not block_type:
        return [], [], []
    try:
        rows = theme_rows(client, block_code)
    except Exception as exc:
        return [], [f"{block_code} theme_market 200744 failed: {type(exc).__name__}: {exc}"], [block_code]
    if not rows:
        return [], [f"{block_code} theme_market 200744 returned no rows"], [block_code]

    members = []
    for row in rows:
        stock_symbol = market_symbol(row.get("N001"), row.get("N002"))
        if stock_symbol:
            members.append({"stock_symbol": stock_symbol, "block_code": block_code})
    return members, [], []


def fetch_index_task(client, task):
    block_code = str(task.get("block_code") or "").strip()
    fallback_name = str(task.get("block_name") or "").strip()
    if not block_code:
        return None, [], [], []
    try:
        table = call_with_retry(client.index_members, block_code)
    except Exception as exc:
        return None, [], [f"{block_code} index_members failed: {type(exc).__name__}: {exc}"], [block_code]

    warnings = []
    info = {
        "block_type": "index",
        "block_name": fallback_name or block_code,
        "block_symbol": block_code,
        "block_code": block_code,
        "parent_code": "",
        "block_level": 1,
    }
    if not table.rows:
        warnings.append(f"{block_code} index_members returned no rows")
        return info, [], warnings, [block_code]

    members = []
    for row in table.rows:
        stock_symbol = str(row.full_code or "").strip()
        if not stock_symbol:
            stock_symbol = market_symbol(row.market_id, row.code)
        if stock_symbol:
            members.append({"stock_symbol": stock_symbol, "block_code": block_code})
    return info, members, warnings, []


def fetch_blocks(tasks_path):
    with open(tasks_path, "r", encoding="utf-8") as handle:
        tasks = json.load(handle)

    client = F10Client(timeout=F10_TIMEOUT)
    infos = {}
    members = {}
    warnings = []
    failed_blocks = set()

    theme_tasks = list(tasks.get("themes") or [])
    index_tasks = list(tasks.get("indices") or [])
    for task in theme_tasks:
        task_members, task_warnings, task_failed = fetch_theme_task(client, task)
        warnings.extend(task_warnings)
        failed_blocks.update(task_failed)
        for member in task_members:
            members[(member["stock_symbol"], member["block_code"])] = member

    for task in index_tasks:
        info, task_members, task_warnings, task_failed = fetch_index_task(client, task)
        warnings.extend(task_warnings)
        failed_blocks.update(task_failed)
        if info:
            infos[info["block_code"]] = info
        for member in task_members:
            members[(member["stock_symbol"], member["block_code"])] = member

    return {
        "blocks": sorted(infos.values(), key=lambda item: item["block_code"]),
        "members": sorted(
            members.values(),
            key=lambda item: (item["block_code"], item["stock_symbol"]),
        ),
        "warnings": sorted(set(warnings)),
        "failed_blocks": sorted(failed_blocks),
    }


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
    elif mode == "blocks":
        if len(sys.argv) < 3:
            raise ValueError("blocks mode requires a task JSON path")
        payload = fetch_blocks(sys.argv[2])
    else:
        raise ValueError("mode must be symbols, sync-blocks, or blocks")
    json.dump(payload, sys.stdout, ensure_ascii=False, separators=(",", ":"))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(f"{type(exc).__name__}: {exc}", file=sys.stderr)
        raise SystemExit(1)
