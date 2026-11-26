.PHONY: run web acceptance test build

web run:
	go run . -web -addr :8080

acceptance:
	go run ./cmd/shepherd -acceptance -runs 64

test:
	go test ./... -count=1

build:
	go build -o bin/shepherd ./cmd/shepherd
