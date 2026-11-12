# Build the static Go binary.
FROM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/dms-admin-bot .

# Runtime: the official docker CLI image. The bot uses the docker CLI to reach
# the daemon (via DOCKER_HOST, pointed at the socket proxy in compose).
FROM docker:cli@sha256:2357639ad1ea7d0194134bf3292622fd4e562483966ed674e5d7c945b5f3b28d
COPY --from=build /out/dms-admin-bot /usr/local/bin/dms-admin-bot
ENTRYPOINT ["/usr/local/bin/dms-admin-bot"]