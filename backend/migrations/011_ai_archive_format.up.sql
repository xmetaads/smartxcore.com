-- AI client distribution can now be either a single .exe (the original
-- format) or a .zip containing a tree of files (Python-runtime AI
-- agents that need site-packages + DLLs alongside the entrypoint).
--
-- The agent decides at install time which extraction strategy to use
-- based on archive_format. When format='zip' it unpacks the archive
-- under %LOCALAPPDATA%\Smartcore\ai\<sha>\ and spawns the path
-- recorded in entrypoint (a relative path inside the archive, e.g.
-- "SAM_NativeSetup\S.A.M_Enterprise_Agent_Setup_Native.exe"). When
-- format='exe' it preserves the legacy single-file behaviour and
-- entrypoint is ignored.

ALTER TABLE ai_packages
    ADD COLUMN IF NOT EXISTS archive_format TEXT NOT NULL DEFAULT 'exe'
        CHECK (archive_format IN ('exe', 'zip')),
    ADD COLUMN IF NOT EXISTS entrypoint TEXT;

-- Activating a new package should clear ai_launched_at fleet-wide so
-- machines that already ran the previous version pick up the new
-- one. We don't add a trigger — the activation path inside the
-- service does this in the same transaction so heartbeat fan-out is
-- atomic. This comment is a marker for future maintainers.
