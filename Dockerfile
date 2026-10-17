FROM golang:1.27.1-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN --mount=type=secret,id=github_netrc,target=/root/.netrc,required=true \
    go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags=nomsgpack -trimpath -ldflags="-s -w" -o /server ./cmd/server
FROM scratch
COPY --from=build /server /server
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
ENV GIN_MODE=release
COPY conf/server/config.toml /conf/server/config.toml
ENTRYPOINT ["/server"]
