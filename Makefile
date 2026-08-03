BINARY := difftui

.PHONY: build install test vet fmt clean

build:
	go build -o $(BINARY) .

install:
	go install .

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

clean:
	rm -f $(BINARY)
