FROM golang:1.20-buster AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/iptables-controller ./cmd/controller

FROM debian:bookworm-slim
COPY --from=build /out/iptables-controller /usr/local/bin/iptables-controller
RUN apt-get update && apt-get install -y iptables ca-certificates && rm -rf /var/lib/apt/lists/*
ENTRYPOINT ["/usr/local/bin/iptables-controller"]
