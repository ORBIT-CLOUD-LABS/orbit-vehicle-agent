# syntax=docker/dockerfile:1

FROM golang:1.27 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -o /out/agent ./cmd/agent

FROM gcr.io/distroless/static-debian12:nonroot
ENV DATA_DIR=/data
VOLUME ["/data"]

COPY --from=build /out/agent /agent

USER nonroot:nonroot
ENTRYPOINT ["/agent"]
