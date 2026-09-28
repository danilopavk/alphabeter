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

# Pasted values often carry stray spaces or newlines; none of these can contain whitespace.
for name in DATABASE_URL S3_ENDPOINT S3_BUCKET S3_ACCESS_KEY_ID S3_SECRET_ACCESS_KEY S3_REGION BACKUP_PREFIX; do
	eval "value=\${$name:-}"
	value=$(printf '%s' "$value" | tr -d '[:space:]')
	eval "$name=\$value"
done

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
response=$(mktemp)
trap 'rm -f "$file" "$response"' EXIT

# The custom format is compressed and restores with pg_restore.
pg_dump --format=custom --no-owner --no-privileges --file "$file" "$DATABASE_URL"

# On failure, show the storage's error (e.g. <Code>SignatureDoesNotMatch</Code>).
if ! curl --silent --show-error --fail-with-body \
	--aws-sigv4 "aws:amz:${S3_REGION:-auto}:s3" \
	--user "$S3_ACCESS_KEY_ID:$S3_SECRET_ACCESS_KEY" \
	--upload-file "$file" --output "$response" \
	"${S3_ENDPOINT%/}/$S3_BUCKET/$key"; then
	echo "backup: upload failed:" >&2
	cat "$response" >&2
	echo >&2
	exit 1
fi

echo "backup: uploaded $key ($(wc -c <"$file" | tr -d ' ') bytes) to bucket $S3_BUCKET"
