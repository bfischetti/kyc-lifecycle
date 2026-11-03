# Builds the webhook-sink, a stand-in Downstream Consumer (cmd/webhook-sink).
# Build context is the repository root; see docker-compose.yml.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# The whole module (minus .dockerignore), so new internal imports keep building.
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /webhook-sink ./cmd/webhook-sink

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /webhook-sink /webhook-sink
EXPOSE 8090
ENTRYPOINT ["/webhook-sink"]
