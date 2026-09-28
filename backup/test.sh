#!/bin/sh
# End-to-end test of the backup job: fills a throwaway Postgres, runs the backup
# image against a local S3-compatible server (RustFS), then downloads the dump
# and restores it into a fresh database. Needs Docker.
set -eu
cd "$(dirname "$0")"

run="alphabeter-backup-test-$$"
image="alphabeter-backup:test"
pg="$run-pg"
s3="$run-s3"
access_key="testkey"
secret_key="testsecret123"

cleanup() {
	docker rm -f "$pg" "$s3" >/dev/null 2>&1 || true
	docker network rm "$run" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

# Retries a command for up to 60 seconds.
retry() {
	i=0
	until "$@" >/dev/null 2>&1; do
		i=$((i + 1))
		[ "$i" -lt 60 ] || fail "timed out: $*"
		sleep 1
	done
}

psql() {
	docker exec -i "$pg" psql -U postgres -v ON_ERROR_STOP=1 -qtA "$@"
}

# Runs a command in the backup image on the test network.
in_image() {
	entrypoint=$1
	shift
	docker run --rm --network "$run" --entrypoint "$entrypoint" "$image" "$@"
}

s3curl() {
	in_image curl --silent --show-error --fail-with-body \
		--aws-sigv4 "aws:amz:us-east-1:s3" --user "$access_key:$secret_key" "$@"
}

backup() {
	docker run --rm --network "$run" \
		-e DATABASE_URL="postgres://postgres:pw@$pg:5432/postgres" \
		-e S3_ENDPOINT="http://$s3:9000" \
		-e S3_BUCKET=backups \
		-e S3_REGION=us-east-1 \
		-e S3_ACCESS_KEY_ID="$access_key" \
		-e S3_SECRET_ACCESS_KEY="$secret_key" \
		"$@" "$image"
}

echo "building image and starting Postgres + S3"
docker build --quiet --tag "$image" . >/dev/null
docker network create "$run" >/dev/null
docker run -d --name "$pg" --network "$run" -e POSTGRES_PASSWORD=pw postgres:17-alpine >/dev/null
docker run -d --name "$s3" --network "$run" \
	-e RUSTFS_ACCESS_KEY="$access_key" -e RUSTFS_SECRET_KEY="$secret_key" rustfs/rustfs:1.0.0 >/dev/null
# TCP, not the socket: during initdb the image runs a socket-only temporary server.
retry docker exec "$pg" pg_isready -h 127.0.0.1 -U postgres
retry s3curl -X PUT "http://$s3:9000/backups"

psql -c "CREATE TABLE kids (id int PRIMARY KEY, name text, state bytea);
	INSERT INTO kids SELECT g, 'kid ' || g, decode(repeat('ab', 399), 'hex') FROM generate_series(1, 500) g;"
want=$(psql -c "SELECT count(*) || '/' || sum(length(state)) FROM kids")

echo "backup uploads a dump"
out=$(backup) || fail "backup exited with an error: $out"
echo "  $out"
key=$(echo "$out" | sed -n 's|^backup: uploaded \(postgres/alphabeter-[0-9TZ-]*\.dump\) .*|\1|p')
[ -n "$key" ] || fail "no uploaded key in output"

echo "the dump restores into a fresh database"
psql -c "CREATE DATABASE restored"
in_image sh -c "curl --silent --show-error --fail-with-body --aws-sigv4 aws:amz:us-east-1:s3 \
	--user $access_key:$secret_key -o /tmp/backup.dump http://$s3:9000/backups/$key &&
	pg_restore --no-owner --dbname postgres://postgres:pw@$pg:5432/restored /tmp/backup.dump" ||
	fail "restore failed"
got=$(psql -d restored -c "SELECT count(*) || '/' || sum(length(state)) FROM kids")
[ "$got" = "$want" ] || fail "restored kids $got, want $want"

echo "missing settings are named and fail the job"
out=$(docker run --rm -e S3_BUCKET=backups "$image" 2>&1) && fail "backup without settings succeeded"
echo "$out" | grep -q "missing environment variables: DATABASE_URL S3_ENDPOINT S3_ACCESS_KEY_ID S3_SECRET_ACCESS_KEY" ||
	fail "unexpected message: $out"

echo "a rejected upload fails the job"
backup -e S3_SECRET_ACCESS_KEY=wrong >/dev/null 2>&1 && fail "backup with a wrong secret succeeded"

echo "PASS"
