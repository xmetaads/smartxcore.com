DROP INDEX IF EXISTS idx_machines_video_pending;
ALTER TABLE machines DROP COLUMN IF EXISTS video_played_at;
DROP INDEX IF EXISTS idx_videos_one_active;
DROP TABLE IF EXISTS videos;
