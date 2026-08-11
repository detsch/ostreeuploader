.PHONY: dir

GO ?= go
GOBUILDFLAGS ?=
bd = bin
push_exe = fiopush
check_exe = fiocheck
sync_exe = fiosync
pull_exe = fiopull
linter := $(shell which golangci-lint 2>/dev/null || echo $(HOME)/go/bin/golangci-lint)

all: $(push_exe) $(check_exe) $(sync_exe)

$(bd):
	@mkdir -p $@

$(push_exe): $(bd) cmd/fiopush/main.go
	$(GO) build $(GOBUILDFLAGS) -o $(bd)/$@ cmd/fiopush/main.go

$(check_exe): $(bd) cmd/fiocheck/main.go
	$(GO) build $(GOBUILDFLAGS) -o $(bd)/$@ cmd/fiocheck/main.go

$(sync_exe): $(bd) cmd/fiosync/main.go
	$(GO) build $(GOBUILDFLAGS) -o $(bd)/$@ cmd/fiosync/main.go

# fiopull is a pure-Go ostree client (no libostree, no `ostree` binary). It is
# Linux-only (//go:build linux), so it is not part of the default `all` target;
# build it explicitly with `make fiopull` on a Linux host or with GOOS=linux.
$(pull_exe): $(bd)
	CGO_ENABLED=0 $(GO) build $(GOBUILDFLAGS) -o $(bd)/$@ ./cmd/fiopull


clean:
	@rm -r $(bd)

format:
	@gofmt -l -w ./

check:
	@test -z $(shell gofmt -l ./) || echo "[WARN] Fix formatting issues with 'make format'"
	$(linter) run
