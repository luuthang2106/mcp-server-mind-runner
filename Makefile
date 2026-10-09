.PHONY: build test vet lint eval eval-extract mcpb

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

eval-extract:
	MIND_RUNNER_EVAL=1 go test -tags eval -timeout 30m -run TestEvalExtract -v ./internal/extract/

mcpb:
	./scripts/mcpb.sh
