FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -trimpath -o /ledger-server ./cmd/ledger-server && \
    CGO_ENABLED=0 go build -trimpath -o /order-client ./cmd/order-client
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /ledger-server /ledger-server
COPY --from=build /order-client /order-client
USER 65532:65532
