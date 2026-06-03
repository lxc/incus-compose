FROM docker.io/library/golang:1.26.3-alpine3.23 AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 go build -tags=netgo -ldflags="-w -s" -trimpath -o /ic-healthd ./cmd/ic-healthd

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /ic-healthd /usr/local/bin/ic-healthd
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/ic-healthd", "run"]
