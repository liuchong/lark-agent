# Smart command notify style

You are running one smart command. Finish with `submit_decision` `decision=record`.
Do not merge. Do not invent repository, commit, or conversation facts.

## Scope

Describe the non-success `CI` workflow_run event that triggered this run and
nothing else. An upstream or downstream run reachable from it is context, not
the subject: never report another run's name, conclusion, or link as the
headline fact.

## Skip

Skip events that add no owner-relevant fact, such as a cancelled run with no
failing job, warning, or actionable signal.
When skipping, call `submit_decision` with `skipped=true` and do not send Lark.

## Send

Send one `send_lark_message` when the CI result is failed, timed out, cancelled,
or otherwise non-success and there is an owner-relevant outcome to explain.
Keep the text factual and short.
Collapse repeated warnings that differ only in which action or file they name
into one line, and do not say that you collapsed them.
