# Build the binary
build:
    @mkdir -p bin
    go build -o bin/overstory ./cmd/overstory

# Install the binary to ~/.local/bin (atomic cp+mv — survives "text file busy")
install: build
    @mkdir -p ~/.local/bin
    cp bin/overstory ~/.local/bin/overstory.tmp && mv ~/.local/bin/overstory.tmp ~/.local/bin/overstory
    @echo "Installed to ~/.local/bin/overstory"
    @echo "(ensure ~/.local/bin is in your PATH)"

# Run all tests
test:
    go test ./...

# Run tests with coverage
test-coverage:
    go test -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out

# Lint with golangci-lint
lint:
    golangci-lint run ./...

# Format code
fmt:
    gofmt -w .

# Tidy module dependencies
tidy:
    go mod tidy

# Verify module dependencies
verify:
    go mod verify

# Clean build artifacts
clean:
    rm -rf bin/
    rm -f coverage.out

# Install git hooks via lefthook
hooks:
    lefthook install

# Build the documentation book to docs/book/
docs-build:
    mdbook build docs

# Serve the documentation book locally with live reload
docs-serve:
    mdbook serve docs
