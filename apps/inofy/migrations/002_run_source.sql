-- S09: §11.2 requires runs to carry the immutable source snapshot
-- and inputs (a queued row must be dispatchable after restart
-- without consulting mutable workflow state — a draft edit can
-- never change an admitted run).
ALTER TABLE runs ADD COLUMN source_json BLOB;
ALTER TABLE runs ADD COLUMN input_json BLOB;
