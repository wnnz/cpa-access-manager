BINARY_NAME := cpa-toolkit
DIST_DIR := dist

.PHONY: test build-linux clean

test:
	go test ./...

build-linux:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildmode=c-shared -o $(DIST_DIR)/$(BINARY_NAME).so ./cmd/cpa-toolkit

clean:
	rm -rf $(DIST_DIR)

