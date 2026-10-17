# vars
BINARY_DIR=bin
APP_NAME=autottv
SERVER_MAIN=./cmd/autottv

# set go global var
export CGO_ENABLED=0
# go related flags
GOFLAGS=-trimpath
LDFLAGS=-ldflags="-s -w"

# AUTO-DETECT OPERATING SYSTEM
# Windows natively injects an OS environment variable set to "Windows_NT"
ifeq ($(OS), Windows_NT)
    EXE_EXT=.exe
else
    EXE_EXT=
endif

# phony targets
.PHONY: local all clean build-local build-win build-linux
# default, 1) clean old bins 2) create new
local: clean build-local

# Build for your current machine's OS
build-local:
	@echo "Building optimized binaries for your current local OS..."
	go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_DIR)/$(APP_NAME)$(EXE_EXT) $(SERVER_MAIN)/main.go
	@echo "Local builds complete."

# Build the Web Server
build-win:
	@echo "Building windows web server..."
	TARGET="windows"; \
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_DIR)/$(APP_NAME)-$$TARGET.exe $(SERVER_MAIN)/main.go
	@echo "Windows web server built successfully"
#
build-linux:
	@echo "Building linux Web Server..."
	TARGET="linux"; \
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_DIR)/$(APP_NAME)-$$TARGET $(SERVER_MAIN)/main.go
	@echo "linux web Server built successfully"
#
build-Apple:
	@echo "Building Apple Web Server..."
	TARGET="darwin"; \
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_DIR)/$(APP_NAME)-$$TARGET $(SERVER_MAIN)/main.go
	@echo "Apple web Server built successfully"

# Remove previously built binaries
clean:
	@echo "Cleaning up old binaries..."
	rm -rf $(BINARY_DIR)

all: clean build-win build-linux build-Apple

# Show help for Makefile
help:
	@echo "Available commands:"
	@echo "  make            - Clean and build for local archatecture"
	@echo "  make all            - Clean and build for all archatecture"
	@echo "  make build-win  - Build only the windows Web Server binary"
	@echo "  make build-linux  - Build only the linux Web Server binary"
	@echo "  make clean      - Delete the build directory"