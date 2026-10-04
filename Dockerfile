FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags=nomsgpack -trimpath -ldflags="-s -w" -o /api ./cmd/api
RUN CGO_ENABLED=0 go build -tags=nomsgpack -trimpath -ldflags="-s -w" -o /migrate ./cmd/migrate
FROM scratch
COPY --from=build /api /api
COPY --from=build /migrate /migrate
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
ENV GIN_MODE=release
ENTRYPOINT ["/api"]
