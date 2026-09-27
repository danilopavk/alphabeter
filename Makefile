export DATABASE_URL ?= postgres://alphabeter:alphabeter@localhost:5432/alphabeter?sslmode=disable
export REDIS_URL ?= redis://localhost:6379/0
export TEST_DATABASE_URL ?= $(DATABASE_URL)
export TEST_REDIS_URL ?= redis://localhost:6379/1
export BASE_URL ?= http://localhost:8090
export COOKIE_SECURE ?= false

.PHONY: up down test run

up:
	docker compose up -d --wait

down:
	docker compose down

test: up
	go test -race ./...

run: up
	go run ./cmd/alphabeter
