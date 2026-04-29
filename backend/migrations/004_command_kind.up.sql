-- Commands now distinguish between two execution kinds:
--
--   * powershell — agent runs the script via powershell.exe -Command -.
--                  Flexible for ad-hoc admin work. Triggers AV heuristics
--                  more often, so prefer 'exec' when possible.
--
--   * exec       — agent runs a named binary directly. The 'script_content'
--                  column holds the path (must live inside
--                  %LOCALAPPDATA%\Smartcore\ for security) and 'script_args'
--                  holds positional arguments. No shell involved → cleaner
--                  for AV/Defender, no string-injection risk, faster.
--
-- Existing rows default to 'powershell' which preserves their semantics.

BEGIN;

CREATE TYPE command_kind AS ENUM ('powershell', 'exec');

ALTER TABLE commands
    ADD COLUMN kind command_kind NOT NULL DEFAULT 'powershell';

COMMIT;
