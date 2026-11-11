BINARY := twenty-backup-tool
GOFLAGS ?=
LDFLAGS ?= -s -w

.PHONY: all build run vet fmt tidy clean install release pkg

all: build

build:
	go build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BINARY) .

run:
	go run . $(ARGS)

vet:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

clean:
	rm -f $(BINARY) $(BINARY)-*

install:
	go install $(GOFLAGS) -ldflags='$(LDFLAGS)' .

pkg:
	makepkg -f

release: clean
	GOOS=linux   GOARCH=amd64 go build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BINARY)-linux-amd64   .
	GOOS=linux   GOARCH=arm64 go build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BINARY)-linux-arm64   .
	GOOS=darwin  GOARCH=amd64 go build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BINARY)-darwin-amd64  .
	GOOS=darwin  GOARCH=arm64 go build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BINARY)-darwin-arm64  .
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BINARY)-windows-amd64.exe .
