#!/usr/bin/env bash
#
# dev-seed.sh — bootstrap a local dev environment with sample data.
# Creates an admin user and an onboarding token you can use to register
# a local agent for end-to-end testing.

set -euo pipefail

DB_URL="${DATABASE_URL:-postgres://worktrack:worktrack@localhost:5432/worktrack?sslmode=disable}"

ADMIN_EMAIL="admin@worktrack.local"
ADMIN_PASS="dev_only_password_change_me"
# bcrypt of "dev_only_password_change_me"
ADMIN_HASH='$2a$10$3hvgGv2HpvMzM4Qfcb1.GuZSMwQR0TZS8dBP7tOGz0JvJ6zVqkCBu'

ONBOARDING_CODE="DEV-$(openssl rand -hex 8 | tr 'a-f' 'A-F')"

psql "$DB_URL" <<SQL
INSERT INTO admin_users (email, password_hash, name, role)
VALUES ('$ADMIN_EMAIL', '$ADMIN_HASH', 'Dev Admin', 'admin')
ON CONFLICT (email) DO UPDATE SET password_hash = EXCLUDED.password_hash;

WITH a AS (SELECT id FROM admin_users WHERE email = '$ADMIN_EMAIL')
INSERT INTO onboarding_tokens (code, employee_email, employee_name, created_by, expires_at)
SELECT '$ONBOARDING_CODE', 'dev-employee@worktrack.local', 'Dev Employee', a.id, NOW() + INTERVAL '7 days'
FROM a;
SQL

echo
echo "Seed complete."
echo "  Admin login:    $ADMIN_EMAIL / $ADMIN_PASS"
echo "  Onboarding code: $ONBOARDING_CODE"
echo
echo "Register a local agent:"
echo "  worktrack-agent.exe -api http://localhost:8080 -register $ONBOARDING_CODE"
