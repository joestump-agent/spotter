BINARY_NAME=spotter-server
MAIN_PATH=./cmd/server/main.go

.PHONY: all deps generate css build run clean docker-build

all: build

deps:
	go mod download
	go install github.com/a-h/templ/cmd/templ@latest
	npm install

generate:
	go generate ./ent
	templ generate

css:
	npx tailwindcss -i ./static/css/input.css -o ./static/css/output.css --minify

build: generate css
	go build -o $(BINARY_NAME) $(MAIN_PATH)

run: build
	./$(BINARY_NAME)

clean:
	rm -f $(BINARY_NAME)
	rm -f ./static/css/output.css

docker-build:
	docker build -t spotter .
