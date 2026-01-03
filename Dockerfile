FROM golang:1.24 AS builder

# Install Node.js
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y nodejs

# Install templ
RUN go install github.com/a-h/templ/cmd/templ@latest

WORKDIR /app

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Flatten directory structure if nested 'spotter' folder exists
# This handles the case where source files were generated into a subdirectory
RUN if [ -d "spotter" ]; then cp -r spotter/* . && rm -rf spotter; fi

# Initialize Tailwind
RUN if [ ! -f "package.json" ]; then npm init -y; fi
RUN npm install -D tailwindcss

# Generate Templates
RUN templ generate

# Generate CSS
RUN npx tailwindcss -i ./static/css/input.css -o ./static/css/output.css --minify

# Build Binary
RUN CGO_ENABLED=1 GOOS=linux go build -v -o spotter-server ./cmd/server

# Runtime Stage
FROM debian:bookworm-slim

WORKDIR /app

# Install certificates
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

# Copy artifacts
COPY --from=builder /app/spotter-server .
COPY --from=builder /app/static ./static

EXPOSE 8080

CMD ["./spotter-server"]
