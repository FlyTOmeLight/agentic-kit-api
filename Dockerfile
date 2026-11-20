ARG KUBECTL_IMAGE=registry.k8s.io/kubectl:v1.36.5@sha256:0667e3f3e6ef435d7cd4c16684244703ebd67314c17511c2a9f87ea44f6cdf6b
FROM ${KUBECTL_IMAGE} AS kubectl
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN --mount=type=secret,id=github_netrc,target=/root/.netrc,required=true \
    go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -tags=nomsgpack -trimpath -ldflags="-s -w" -o /server ./cmd/server
FROM scratch AS api
COPY --from=build /server /server
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
ENV GIN_MODE=release
COPY conf/server/config.toml /conf/server/config.toml
ENTRYPOINT ["/server"]

FROM api AS sandbox
COPY --from=kubectl /bin/kubectl /bin/kubectl

FROM api AS default
