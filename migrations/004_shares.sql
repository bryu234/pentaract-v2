CREATE TABLE file_shares (
 id uuid PRIMARY KEY,
 node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 token_hash bytea NOT NULL UNIQUE,
 token_enc bytea NOT NULL,
 expires_at timestamptz NOT NULL,
 one_time boolean NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 used_at timestamptz,
 revoked_at timestamptz
);
CREATE INDEX file_shares_node ON file_shares(node_id);
CREATE FUNCTION revoke_trashed_shares() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL THEN
  WITH RECURSIVE tree AS (SELECT NEW.id AS id UNION ALL SELECT n.id FROM nodes n JOIN tree t ON n.parent_id=t.id)
  UPDATE file_shares SET revoked_at=COALESCE(revoked_at,now()) WHERE node_id IN(SELECT id FROM tree);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER revoke_trashed_shares AFTER UPDATE OF deleted_at ON nodes FOR EACH ROW EXECUTE FUNCTION revoke_trashed_shares();
