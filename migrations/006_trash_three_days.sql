-- Apply the three-day limit to entries already in the trash as well.
UPDATE nodes SET purge_after=LEAST(COALESCE(purge_after, deleted_at + interval '3 days'), deleted_at + interval '3 days') WHERE deleted_at IS NOT NULL;
