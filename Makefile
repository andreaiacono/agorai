APP := agorai

.PHONY: run build mac mac-arm mac-intel tidy clean

run: ## run locally
	go run .

build: ## build for this machine
	go build -o bin/$(APP) .

mac-arm: ## cross-compile for Apple Silicon
	GOOS=darwin GOARCH=arm64 go build -o bin/$(APP)-darwin-arm64 .

mac-intel: ## cross-compile for Intel Macs
	GOOS=darwin GOARCH=amd64 go build -o bin/$(APP)-darwin-amd64 .

mac: mac-arm mac-intel ## build both Mac binaries

tidy: ## resolve module deps (creates go.sum)
	go mod tidy

clean:
	rm -rf bin
