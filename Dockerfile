# Stage 1: Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o ./llm-mux \
    ./cmd/server/

# Stage 2: Runtime stage
FROM alpine:3.23

RUN apk add --no-cache tzdata ca-certificates

RUN addgroup -g 1000 llm-mux && \
    adduser -D -u 1000 -G llm-mux llm-mux && \
    mkdir -p /llm-mux && \
    chown -R llm-mux:llm-mux /llm-mux

WORKDIR /llm-mux

COPY --from=builder --chown=llm-mux:llm-mux /build/llm-mux ./
COPY --chown=llm-mux:llm-mux config.example.yaml ./

USER llm-mux
ENV TZ=UTC
EXPOSE 8317

CMD ["./llm-mux"]
