.PHONY: build test vet install demo demo-fixture demo-record demo-gif clean

build:
	CGO_ENABLED=0 go build -o bin/agb ./cmd/agb

test:
	go test ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt needed"; exit 1; }

# Puts `agb` in ~/.local/bin and removes obsolete command names.
install:
	bash ./scripts/install-local.sh

# The picker against synthetic sessions, opening stubbed out.
demo:
	bash ./demo/run.sh

demo-fixture:
	go run ./demo/fixture

# DEMO=push or DEMO=pull records those instead of the picker tour.
demo-record:
	bash ./demo/record.sh $(DEMO)

demo-gif:
	bash ./demo/gif.sh $(DEMO)

clean:
	rm -rf bin demo/.fixture
