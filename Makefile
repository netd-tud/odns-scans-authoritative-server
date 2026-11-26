GO ?= go

.PHONY: build clean

build:
	$(GO) build -o rr-mirror .

clean:
	rm -f rr-mirror
