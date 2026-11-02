VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test race vet fmt fmt-check check docker clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o kubestatus ./cmd/kubestatus

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

check: fmt-check vet race build

docker:
	docker build --build-arg VERSION=$(VERSION) -t kubestatus:$(VERSION) .

clean:
	rm -f kubestatus
