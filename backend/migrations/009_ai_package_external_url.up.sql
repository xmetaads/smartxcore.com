-- Allow registering an AI package whose bytes live on a CDN (Bunny,
-- Cloudflare R2, S3, …) instead of the VPS local disk.
--
-- Why: the AI client is ~35 MB and a CDN with edge points-of-presence
-- close to the employee gives a 5-10× faster first-byte time than the
-- single VPS in Germany or wherever the backend lives. Admins upload
-- the binary to their CDN bucket once, then register the URL +
-- SHA256 + size in this table — no file bytes ever touch the
-- backend disk.
--
-- When external_url IS NOT NULL the agent pulls from that URL.
-- When NULL the agent pulls /downloads/ai-client.exe served by the
-- VPS nginx (current behaviour preserved for small deployments that
-- don't need a CDN).

BEGIN;

ALTER TABLE ai_packages ADD COLUMN external_url TEXT;

COMMIT;
