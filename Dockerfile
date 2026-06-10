# ABOUTME: Two-stage build: static Go binary on distroless, nonroot.
# ABOUTME: Image size is irrelevant to Cloud Run cold start (image streaming), correctness is not.

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /cadenza ./cmd/cadenza

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /cadenza /cadenza
USER nonroot
ENTRYPOINT ["/cadenza"]
