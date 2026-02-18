#!/usr/bin/env python3
import json
import os
import sys
import urllib.error
import urllib.request


def fail(msg: str, code: int = 1) -> None:
    print(f"ERROR: {msg}", file=sys.stderr)
    raise SystemExit(code)


def parse_problem_id(raw: str) -> int:
    if raw is None or raw.strip() == "":
        fail("PROBLEM_ID 为空，请先设置有效数字，例如: export PROBLEM_ID=1")
    value = raw.strip()
    if not value.isdigit():
        fail(f"PROBLEM_ID 不是数字: {value!r}")
    pid = int(value)
    if pid <= 0:
        fail(f"PROBLEM_ID 必须大于 0，当前值: {pid}")
    return pid


def pretty_json(text: str) -> str:
    try:
        obj = json.loads(text)
        return json.dumps(obj, ensure_ascii=False, indent=2)
    except Exception:
        return text


def main() -> None:
    api_base = os.getenv("API_BASE", "http://127.0.0.1:18080").rstrip("/")
    token = os.getenv("TOKEN", "").strip()
    problem_id = parse_problem_id(os.getenv("PROBLEM_ID"))

    if token == "":
        fail("TOKEN 为空，请先设置 Bearer Token，例如: export TOKEN=xxx")

    payload = {
        "problem_id": problem_id,
        "language": 1,
        "code": "#include <iostream>\nint main(){return 0;}",
        "time_limit": 1000,
        "memory_limit": 65536,
    }

    # 使用 json.dumps 生成合法 JSON
    body = json.dumps(payload, ensure_ascii=False)

    # 调试输出：确认请求体完整且合法
    print("=== Request JSON Body ===")
    print(pretty_json(body))
    print("=========================")

    url = f"{api_base}/api/v1/submit"
    req = urllib.request.Request(
        url=url,
        data=body.encode("utf-8"),
        method="POST",
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
        },
    )

    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            resp_text = resp.read().decode("utf-8", errors="replace")
            print(f"HTTP {resp.status}")
            print("=== Response Body ===")
            print(pretty_json(resp_text))
            print("=====================")
            return
    except urllib.error.HTTPError as e:
        resp_text = e.read().decode("utf-8", errors="replace")
        print(f"HTTP {e.code} {e.reason}", file=sys.stderr)
        print("=== Error Response Body ===", file=sys.stderr)
        print(pretty_json(resp_text), file=sys.stderr)
        print("===========================", file=sys.stderr)
        if e.code == 400:
            print(
                "DEBUG: 收到 400，请检查返回 JSON 中的 error/code/details 字段定位具体参数问题。",
                file=sys.stderr,
            )
        raise SystemExit(1)
    except urllib.error.URLError as e:
        fail(f"请求失败，网络或服务不可达: {e}")


if __name__ == "__main__":
    main()
