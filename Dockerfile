FROM golang:1.26-alpine AS builder
RUN apk add --no-cache wireguard-tools iptables iproute2
COPY core/ /build/core/
COPY downloader-native-torrent/ /build/downloader-native-torrent/
WORKDIR /build/downloader-native-torrent
RUN go mod download
RUN CGO_ENABLED=0 go build -o /downloader-native-torrent ./cmd/module

FROM alpine:3.21
RUN apk add --no-cache wireguard-tools iptables iproute2 ca-certificates tzdata
COPY --from=builder /downloader-native-torrent /
ENTRYPOINT ["/downloader-native-torrent"]
