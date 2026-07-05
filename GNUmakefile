default: build

BINARY := terraform-provider-iaas

.PHONY: build
build:
	go build -o $(BINARY) .

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

# Build a local release with GoReleaser to test the .goreleaser.yml config
# (archives, checksums, signing). Requires goreleaser on PATH; skips with a
# hint when it is not installed. No artifacts are published.
.PHONY: release-snapshot
release-snapshot:
	@command -v goreleaser >/dev/null 2>&1 || { \
		echo "goreleaser not installed - see https://goreleaser.com/install/"; \
		echo "  (e.g. go install github.com/goreleaser/goreleaser/v2@latest)"; \
		exit 1; \
	}
	goreleaser release --snapshot --clean
