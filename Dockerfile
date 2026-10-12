# syntax=docker/dockerfile:1

FROM golang:1.27 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -o /out/agent ./cmd/agent
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
ENV DATA_DIR=/data

COPY --from=build /out/agent /agent
# 볼륨을 선언하기 전에 nonroot 소유로 만들어 둔다. 이미지에 없는 경로를
# VOLUME으로 선언하면 root 소유로 생겨 nonroot가 상태 파일을 쓰지 못한다.
COPY --from=build --chown=nonroot:nonroot /out/data /data
VOLUME ["/data"]

USER nonroot:nonroot
ENTRYPOINT ["/agent"]
