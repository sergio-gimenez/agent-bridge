.PHONY: build test vet install demo demo-fixture demo-record demo-gif docs docs-serve clean

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

# DEMO=push, DEMO=pull or DEMO=actions records those instead of the picker tour.
demo-record:
	bash ./demo/record.sh $(DEMO)

demo-gif:
	bash ./demo/gif.sh $(DEMO)

DOCS_VENV := .venv-docs

# The docs site into site/, as .github/workflows/docs.yml publishes it.
docs: $(DOCS_VENV)
	ZENSICAL=$(DOCS_VENV)/bin/zensical bash ./scripts/build-docs.sh build

# Live preview; edits to docs/ and README.md show up as you save them.
docs-serve: $(DOCS_VENV)
	ZENSICAL=$(DOCS_VENV)/bin/zensical bash ./scripts/build-docs.sh serve

$(DOCS_VENV): requirements-docs.txt
	python3 -m venv $(DOCS_VENV)
	$(DOCS_VENV)/bin/pip install -q -r requirements-docs.txt
	touch $(DOCS_VENV)

clean:
	rm -rf bin demo/.fixture site .site-src
