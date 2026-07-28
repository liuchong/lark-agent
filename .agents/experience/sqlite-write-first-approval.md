# SQLite approval decisions must write first

When an operator process updates the same WAL database as a running daemon, a
deferred transaction that reads and then writes can fail with
`SQLITE_BUSY_SNAPSHOT`: the read establishes a snapshot, another connection
commits, and the stale snapshot cannot be upgraded to a writer. `busy_timeout`
does not make that snapshot upgrade valid.

For exact state transitions such as approval decisions, make the first
transaction statement a conditional write and return the owning row data from
that write:

```sql
UPDATE action_attempts
SET status = ?, updated_at = ?
WHERE id = ? AND status = ?
RETURNING work_item_id
```

This requests the write lock before a read snapshot is established, preserves
the compare-and-set precondition, and lets the configured busy timeout handle a
brief concurrent writer. Test it with one connection holding a write
transaction while a second connection starts the transition, then release the
writer and assert that both approve and reject complete atomically.
