ALTER TABLE nodes ADD COLUMN recovery_pending boolean NOT NULL DEFAULT false;
CREATE TABLE recovery_status (id integer PRIMARY KEY CHECK(id=1), storage_id uuid NOT NULL, revision bigint NOT NULL DEFAULT 1, published_revision bigint NOT NULL DEFAULT 0, published_at timestamptz, error text, paused boolean NOT NULL DEFAULT false);
INSERT INTO recovery_status(id,storage_id) VALUES(1,gen_random_uuid());
CREATE FUNCTION recovery_changed() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 UPDATE recovery_status SET revision=revision+1 WHERE id=1;
 RETURN NULL;
END $$;
CREATE TRIGGER recovery_nodes AFTER INSERT OR UPDATE OF parent_id,name,state,deleted_at,purge_after,plain_hash OR DELETE ON nodes FOR EACH STATEMENT EXECUTE FUNCTION recovery_changed();
CREATE TRIGGER recovery_chunks AFTER INSERT OR DELETE ON file_chunks FOR EACH STATEMENT EXECUTE FUNCTION recovery_changed();

CREATE TRIGGER recovery_telegram AFTER INSERT OR UPDATE ON telegram_settings FOR EACH STATEMENT EXECUTE FUNCTION recovery_changed();
