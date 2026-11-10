"""Fetch TDX online metadata through the eltdx Python library."""

import json
import sys

try:
    from eltdx import TdxClient
    from eltdx.hosts import DEFAULT_HOSTS
    from eltdx.mac import BoardType, MacClient, PresetField, SortOrder, SortType
except ImportError:
    print(
        'eltdx is required: python3 -m pip install "eltdx==3.3.5" '
        '(Windows: python -m pip install "eltdx==3.3.5")',
        file=sys.stderr,
    )
    raise SystemExit(1)


BOARD_TYPES = (
    (BoardType.HY, "industry", 1),
    (BoardType.HY2, "industry", 2),
    (BoardType.GN, "concept", 1),
    (BoardType.FG, "style", 1),
    (BoardType.DQ, "region", 1),
    (BoardType.YJ_LEVEL1, "tdx_research", 1),
    (BoardType.YJ_LEVEL2, "tdx_research", 2),
    (BoardType.YJ_LEVEL3, "tdx_research", 3),
)

MARKET_PREFIX = {0: "sz", 1: "sh", 2: "bj"}
TDX_TIMEOUT = 10
TDX_HOST_ATTEMPTS = 8


def market_symbol(market, code):
    prefix = MARKET_PREFIX.get(int(market))
    code = str(code or "").strip()
    if not prefix or not code:
        return ""
    return prefix + code


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
                rows = client.codes.all_markets()
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


def fetch_blocks():
    infos = {}
    members = {}
    with MacClient(timeout=20) as client:
        for board_type, block_type, level in BOARD_TYPES:
            for board in client.get_board_list(board_type, count=10000):
                board_code = str(board.code or "").strip()
                if not board_code:
                    continue
                block_symbol = market_symbol(board.market, board_code)
                if not block_symbol:
                    block_symbol = "sh" + board_code
                block_name = str(board.name or "").strip()
                if not block_name:
                    continue
                infos.setdefault(
                    block_symbol,
                    {
                        "block_type": block_type,
                        "block_name": block_name,
                        "block_symbol": block_symbol,
                        "block_code": block_symbol,
                        "parent_code": "",
                        "block_level": level,
                    },
                )
                for member in client.get_board_members(
                    board_code,
                    count=5000,
                    sort_type=SortType.CODE,
                    sort_order=SortOrder.NONE,
                    fields=PresetField.NONE,
                ):
                    stock_symbol = market_symbol(member.market, member.code)
                    if not stock_symbol:
                        continue
                    members[(stock_symbol, block_symbol)] = {
                        "stock_symbol": stock_symbol,
                        "block_code": block_symbol,
                    }
    return {"blocks": list(infos.values()), "members": list(members.values())}


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else ""
    if mode == "symbols":
        payload = fetch_symbols()
    elif mode == "blocks":
        payload = fetch_blocks()
    else:
        raise ValueError("mode must be symbols or blocks")
    json.dump(payload, sys.stdout, ensure_ascii=False, separators=(",", ":"))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(f"{type(exc).__name__}: {exc}", file=sys.stderr)
        raise SystemExit(1)
