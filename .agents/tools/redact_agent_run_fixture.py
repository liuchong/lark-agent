#!/usr/bin/env python3
"""Redact an exported agent run transcript into a regression fixture skeleton."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
from pathlib import Path
from typing import Any


SECRET_PATTERNS = [
    re.compile(r"(?i)bearer\s+[a-z0-9._\-]+"),
    re.compile(r"(?i)(api[_-]?key|token|secret)\s*[:=]\s*['\"]?[^'\"\s,}]+"),
    re.compile(r"sk-[a-zA-Z0-9_\-]{12,}"),
]


def redact_text(value: str) -> str:
    redacted = value
    for pattern in SECRET_PATTERNS:
        redacted = pattern.sub("[REDACTED_SECRET]", redacted)
    return redacted


def digest(value: str) -> dict[str, Any]:
    value = redact_text(value or "")
    return {
        "bytes": len(value.encode("utf-8")),
        "sha256": hashlib.sha256(value.encode("utf-8")).hexdigest(),
    }


def redact_run(data: dict[str, Any]) -> dict[str, Any]:
    return {
        "id": data.get("id"),
        "work_item_id": data.get("work_item_id"),
        "status": data.get("status"),
        "role": data.get("role"),
        "profile": data.get("profile"),
        "provider": data.get("provider"),
        "protocol": data.get("protocol"),
        "model": data.get("model"),
        "model_fingerprint": data.get("model_fingerprint"),
        "config_fingerprint": data.get("config_fingerprint"),
        "last_error": redact_text(data.get("last_error", "")),
    }


def redact_step(data: dict[str, Any]) -> dict[str, Any]:
    return {
        "sequence": data.get("sequence"),
        "kind": data.get("kind"),
        "phase": data.get("phase"),
        "attempt": data.get("attempt"),
        "tool_name": data.get("tool_name"),
        "request_id": data.get("request_id"),
        "finish_reason": data.get("finish_reason"),
        "http_status": data.get("http_status"),
        "failure_category": data.get("failure_category"),
        "recovery_action": data.get("recovery_action"),
        "prompt_tokens": data.get("prompt_tokens"),
        "completion_tokens": data.get("completion_tokens"),
        "error": redact_text(data.get("error", "")),
        "input": digest(data.get("input_json", "")),
        "output": digest(data.get("output_json", "")),
    }


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Convert agent run JSONL transcript to a redacted fixture skeleton."
    )
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--source-work-id", default="")
    parser.add_argument("--failure", default="")
    parser.add_argument("--expected-terminal-state", default="")
    args = parser.parse_args()

    fixture: dict[str, Any] = {
        "source_work_id": args.source_work_id,
        "failure": args.failure,
        "expected_terminal_state": args.expected_terminal_state,
        "run": None,
        "steps": [],
    }
    for line in args.input.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        record = json.loads(line)
        kind = record.get("kind")
        data = record.get("data") or {}
        if kind == "run":
            fixture["run"] = redact_run(data)
        elif kind == "step":
            fixture["steps"].append(redact_step(data))

    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(
        json.dumps(fixture, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
