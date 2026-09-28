# The backup job (see backup.sh). pg_dump comes from the official Postgres image;
# its major version must be at least the server's. Production (Railway) runs 18;
# keep test.sh's Postgres on the same version.
ARG PG_MAJOR=18
FROM postgres:${PG_MAJOR}-alpine

RUN apk add --no-cache curl
COPY backup.sh /usr/local/bin/backup

USER postgres
ENTRYPOINT ["/bin/sh", "/usr/local/bin/backup"]
