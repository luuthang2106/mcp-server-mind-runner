.PHONY: build test vet lint eval mcpb

build:
	go build -o dist/mind-runner ./cmd/mind-runner

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run

eval:
	MIND_RUNNER_EVAL=1 go test -tags eval -run TestEval -v ./internal/brain/

mcpb:
	./scripts/mcpb.sh
