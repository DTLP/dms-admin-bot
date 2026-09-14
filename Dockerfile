# Build the static Go binary.
FROM golang:1.27-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/dms-admin-bot .

# Runtime: the official docker CLI image. The bot uses the docker CLI to reach
# the daemon (via DOCKER_HOST, pointed at the socket proxy in compose).
FROM docker:cli@sha256:eccaacfeed644c7de222ff047483568cb988dde95476fbaaf10ea2d04921bb66
COPY --from=build /out/dms-admin-bot /usr/local/bin/dms-admin-bot
ENTRYPOINT ["/usr/local/bin/dms-admin-bot"]