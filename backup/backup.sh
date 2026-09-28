#!/bin/sh
# Dumps the Postgres database and uploads it to S3-compatible storage (Cloudflare
# R2 in production). Runs as a Railway cron job: one backup, then it exits; a
# non-zero exit marks the run as failed.
#
# Required: DATABASE_URL, S3_ENDPOINT (e.g. https://<account-id>.r2.cloudflarestorage.com),
#           S3_BUCKET, S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY
# Optional: S3_REGION (default "auto", as R2 expects), BACKUP_PREFIX (default "postgres")
#
# To restore, download a .dump file and run:
#   pg_restore --clean --if-exists --no-owner --dbname "$DATABASE_URL" alphabeter-<time>.dump
set -eu

missing=""
for name in DATABASE_URL S3_ENDPOINT S3_BUCKET S3_ACCESS_KEY_ID S3_SECRET_ACCESS_KEY; do
	eval "value=\${$name:-}"
	[ -n "$value" ] || missing="$missing $name"
done
if [ -n "$missing" ]; then
	echo "backup: missing environment variables:$missing" >&2
	exit 1
fi

key="${BACKUP_PREFIX:-postgres}/alphabeter-$(date -u +%Y-%m-%dT%H%M%SZ).dump"
file=$(mktemp)
trap 'rm -f "$file"' EXIT

# The custom format is compressed and restores with pg_restore.
pg_dump --format=custom --no-owner --no-privileges --file "$file" "$DATABASE_URL"

curl --silent --show-error --fail-with-body \
	--aws-sigv4 "aws:amz:${S3_REGION:-auto}:s3" \
	--user "$S3_ACCESS_KEY_ID:$S3_SECRET_ACCESS_KEY" \
	--upload-file "$file" \
	"${S3_ENDPOINT%/}/$S3_BUCKET/$key" >/dev/null

echo "backup: uploaded $key ($(wc -c <"$file" | tr -d ' ') bytes) to bucket $S3_BUCKET"
