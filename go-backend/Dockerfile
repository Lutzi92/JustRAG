FROM golang:1.26-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG BUILD_VERSION=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X main.version=${BUILD_VERSION}" -o /server ./cmd/server/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /worker ./cmd/worker/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /migrate ./cmd/migrate/

FROM alpine:3.20
RUN apk --no-cache add ca-certificates curl poppler-utils
WORKDIR /app
COPY --from=builder /server  /app/server
COPY --from=builder /worker  /app/worker
COPY --from=builder /migrate /app/migrate

EXPOSE 3000
CMD ["/app/server"]
