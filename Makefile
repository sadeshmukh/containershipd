.PHONY: build run tidy clean

build:
	go build -o bin/containershipd .

run:
	go run .

tidy:
	go mod tidy

clean:
	rm -rf bin/
