GOVULNCHECK_VERSION := v1.3.0
GOSEC_VERSION := v2.26.1

.PHONY: fmt test race vet vuln gosec security eval verify build run

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
