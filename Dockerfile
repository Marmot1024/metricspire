# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build

RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/metricspire ./cmd/metricspire

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/metricspire /metricspire
COPY LICENSE THIRD_PARTY_NOTICES.md /licenses/

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/metricspire"]
CMD ["serve", "--config", "/etc/metricspire/runtime.yaml"]
