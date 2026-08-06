# Agent Tools

## `redact_agent_run_fixture.py`

Convert an exported agent run transcript into a redacted regression fixture
skeleton.

```bash
python3 .agents/tools/redact_agent_run_fixture.py \
  --input /tmp/run.jsonl \
  --output integration_test/lark_agent/testdata/harness_cases/work_5994.json \
  --source-work-id 5994 \
  --failure "tool_choice required with thinking" \
  --expected-terminal-state dead_letter
```

The tool preserves provider/profile/protocol, phase, attempts, finish reason,
request ID, failure category, recovery action, tool names, token counts, and
byte/digest summaries. It does not copy raw private chat text, tool output,
API keys, tokens, secrets, or model thinking into fixtures.
