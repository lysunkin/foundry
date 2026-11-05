BINARY     := foundry-registry
CTL_BINARY := regctl
DATA_DIR   := ./data
ADDR       := :8080

.PHONY: all build run test clean tidy

all: build

build:
	go build -o $(BINARY) ./cmd/registry
	go build -o $(CTL_BINARY) ./cmd/regctl

run: build
	./$(BINARY) -data $(DATA_DIR) -addr $(ADDR)

tidy:
	go mod tidy

test:
	go test ./... -count=1

clean:
	rm -f $(BINARY) $(CTL_BINARY)
	rm -rf $(DATA_DIR)

# Quick Docker push test (requires daemon with insecure-registries)
test-push:
	docker tag nginx localhost$(ADDR)/nginx:test
	docker push localhost$(ADDR)/nginx:test
	docker pull localhost$(ADDR)/nginx:test
