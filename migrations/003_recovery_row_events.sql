-- Empty worker polling statements must not schedule catalog publications.
DROP TRIGGER recovery_nodes ON nodes;
DROP TRIGGER recovery_chunks ON file_chunks;
DROP TRIGGER recovery_telegram ON telegram_settings;
CREATE TRIGGER recovery_nodes AFTER INSERT OR UPDATE OF parent_id,name,state,deleted_at,purge_after,plain_hash OR DELETE ON nodes FOR EACH ROW EXECUTE FUNCTION recovery_changed();
CREATE TRIGGER recovery_chunks AFTER INSERT OR DELETE ON file_chunks FOR EACH ROW EXECUTE FUNCTION recovery_changed();
CREATE TRIGGER recovery_telegram AFTER INSERT OR UPDATE ON telegram_settings FOR EACH ROW EXECUTE FUNCTION recovery_changed();
