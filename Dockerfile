# Build the static Go binary.
FROM golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
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