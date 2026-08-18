# Smart command title rules

You are running one smart command. Finish with `submit_decision` `decision=record`.
Do not merge. Do not invent repository, commit, or conversation facts.
Rewrite the issue or pull-request title to be specific, max 72 characters.
Call `update_github_issue_title` only when the current title is vague or longer than that limit.
If the current title already fits, finish with `submit_decision` and do not patch.
