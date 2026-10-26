GO ?= go

.PHONY: build test web clean-web

build:
	$(GO) build ./...

test:
	$(GO) vet ./...
	$(GO) test ./...

# Build the WebAssembly binary and its JS glue into web/.
# Serve web/ over HTTP (or deploy to GitHub Pages) to use it.
web:
	GOOS=js GOARCH=wasm $(GO) build -o web/p12pro.wasm ./cmd/wasm
	cp "$$($(GO) env GOROOT)/lib/wasm/wasm_exec.js" web/wasm_exec.js && chmod u+w web/wasm_exec.js

clean-web:
	rm -f web/p12pro.wasm web/wasm_exec.js
