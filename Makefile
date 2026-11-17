BIN      := build/qqbot_bridge
PREFIX   ?= $(HOME)/.local/bin
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILDTIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -X main.version=$(VERSION) -X main.buildTime=$(BUILDTIME)

.PHONY: all build bind run start stop test test-short fmt vet clean install

all: build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) .

bind:
	go run . bind

run:
	go run . -f

# 后台运行（默认行为），停止用 make stop
start: build
	./$(BIN)

stop:
	./$(BIN) -stop

# 含一次真实 codebuddy 调用（约 10 秒）
test:
	go test ./...

# 跳过真实调用，适合本地快速验证
test-short:
	go test -short ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

clean:
	rm -rf build

install: build
	install -d $(PREFIX)
	install -m755 $(BIN) $(PREFIX)/qqbot_bridge
