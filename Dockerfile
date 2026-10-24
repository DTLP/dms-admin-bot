# Build the static Go binary.
FROM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/dms-admin-bot .

# Runtime: the official docker CLI image. The bot uses the docker CLI to reach
# the daemon (via DOCKER_HOST, pointed at the socket proxy in compose).
FROM docker:cli@sha256:5e6baf67325a79f64ed3bed6863954d5e05d0f3d4e57cbdb14809346814bbfdb
COPY --from=build /out/dms-admin-bot /usr/local/bin/dms-admin-bot
ENTRYPOINT ["/usr/local/bin/dms-admin-bot"]