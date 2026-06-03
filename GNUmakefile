default: build

.PHONY: build
build:
	go build ./...

.PHONY: test
test:
	go test ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: fmt
fmt:
	gofmt -w .

.PHONY: tools
tools:
	go install github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@latest

.PHONY: docs
docs:
	@command -v tfplugindocs >/dev/null 2>&1 || $(MAKE) tools
	tfplugindocs generate --provider-name iaas
