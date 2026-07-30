# Contextual delegated investigation

An ambiguous handoff sentence is not necessarily the work subject. Build one
bounded, digest-backed same-chat snapshot and reuse it for semantic owner-answer
resolution, task classification, and the main Agent. The snapshot must include
short pre-target conversation direction, the exact target, post-target
clarifications through a fixed cutoff, sender display names, message types, and
typed attachment readability.

Natural follow-ups such as "当前项目", "这个项目", "该项目", "current project",
or "this project" are explicit references to the most recent same-sender
repository path in that bounded window. Scope extraction must recognize these
phrases before the model sees tools; otherwise the model can read the right
chat context yet still search a similarly named sibling repository.

For delegated investigation:

- classify the concrete subject from the whole snapshot, not the last noun in
  the target;
- preserve the distinction between evidence observed before and after the
  target;
- count only unique, nonempty, task-relevant reads as completed work;
- treat an unreadable image as explicit missing evidence, never empty success;
- persist the investigation before any progress promise;
- use separate durable keys for owner notice, progress, and final reply;
- recheck whether the owner handled the request before final send;
- require a result, owner-handled closure, or explicit blocked closure;
- show the investigation subject, status, context evidence state, last error,
  and exact next command in the owner-private task control plane.

Acceptance must include a real project source question and a false-premise
trap. A current-source conclusion may identify a deployment-version mismatch
as the next check, but it must not claim a production deployment fact that was
not observed.
