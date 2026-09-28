.PHONY: build test vet install demo demo-fixture demo-record demo-gif clean

build:
	go build -o bin/ocs ./cmd/ocs

test:
	go test ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt needed"; exit 1; }

# Puts `ocs` in ~/.local/bin, no root needed.
install:
	bash ./scripts/install-local.sh

# The picker against synthetic sessions, opening stubbed out.
demo:
	bash ./demo/run.sh

demo-fixture:
	go run ./demo/fixture

demo-record:
	bash ./demo/record.sh

demo-gif:
	bash ./demo/gif.sh

clean:
	rm -rf bin demo/.fixture
