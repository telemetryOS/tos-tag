GOVULNCHECK_VERSION := v1.6.0
GOSEC_VERSION := v2.28.0
COMPOSE := ./container/docker-compose.sh

.PHONY: fmt test race vet vuln gosec security eval verify build run run-live container-build container-bootstrap container-up container-shell container-opencode

fmt:
	gofmt -w $$(rg --files -g '*.go')

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

gosec:
	go run github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION) ./...

security: gosec vuln

eval:
	go test ./evals -run TestBehavioralEval -count=1
	go run -buildvcs=false ./cmd/eval

verify: fmt test race vet eval security

build:
	go build -buildvcs=false ./cmd/api ./cmd/admin ./cmd/eval

run:
	go run ./cmd/api

run-live:
	@test -f runtime.env || { echo "runtime.env is required; copy runtime.env.example and fill the live Slack values" >&2; exit 1; }
	@set -a; . ./runtime.env; set +a; exec go run ./cmd/api

container-build:
	$(COMPOSE) build workspace

container-bootstrap:
	$(COMPOSE) run --rm workspace bootstrap-workspace --sync --update

container-up:
	$(COMPOSE) --profile workspace --profile stack up -d mongo workspace tag

container-shell:
	$(COMPOSE) exec workspace bash

container-opencode:
	$(COMPOSE) exec workspace opencode
