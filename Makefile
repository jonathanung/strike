.PHONY: build run run-echo test vet clean setup

build:
	go build -o strike ./cmd/strike

# Creates ~/.strike (config, example agent + skill); never overwrites.
setup:
	bash scripts/setup.sh

# Runs with your configured/default provider (needs credentials; see README).
run: build
	./strike

# Offline dev loop — no API key needed.
run-echo: build
	./strike --provider echo

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f strike
