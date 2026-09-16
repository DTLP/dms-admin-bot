# dms-admin-bot

A Telegram bot that manages aliases on **[docker-mailserver](https://docker-mailserver.github.io)**.
It is specific to docker-mailserver — it relies on the `setup` CLI and the
`postfix-virtual.cf` database, so it will not work with any other mail server.
It exposes list/add/delete operations over Telegram, restricted to a single
owner.

Written in Go; it runs as a container or a bare binary on the same host as the
mail server.

## How it works

The bot talks to the Docker daemon (through a restricted socket proxy by
default). It reads the alias database (`postfix-virtual.cf`) directly and
executes `docker exec <container> setup alias <add|del>` for changes, which is
how docker-mailserver manages aliases natively.

Because the bot only makes outbound connections to the Telegram API
(long polling), no inbound ports or VPN access are required.

## Usage

| Command                           | Action                                                            |
| --------------------------------- | ----------------------------------------------------------------- |
| `/alias_list [mailbox]`           | List aliases (sorted, paginated), optionally only for one mailbox |
| `/alias_add <alias> <mailbox>`    | Add `alias@domain` → `mailbox`                                    |
| `/alias_delete <alias> <mailbox>` | Remove an alias → mailbox mapping (asks for confirmation)         |

`alias` is the local part of an address; the domain is appended from
`MAIL_DOMAIN`. `mailbox` is either a local part (an existing account or alias,
which is verified before adding) or a full external address such as
`someone@external.example`. For example, with `MAIL_CONTAINER=mail`, `/alias_add
support admin` is equivalent to:

```sh
docker exec mail setup alias add support@example.com admin@example.com
```

## Setup and running

The image is published to the GitHub Container Registry as
`ghcr.io/dtlp/dms-admin-bot` — `latest` plus version tags (a git tag `v1.0.0`
produces `v1.0.0` and `v1.0`).

1. Create a bot with [@BotFather](https://t.me/BotFather) and note the token.
2. Get your numeric user id from [@userinfobot](https://t.me/userinfobot).
3. Start the bot using the method you prefer (see below), providing these
   required environment variables:

| Variable         | Required | Description                                     |
| ---------------- | -------- | ----------------------------------------------- |
| `BOT_TOKEN`      | yes      | Telegram bot token from @BotFather              |
| `BOT_USER_ID`    | yes      | Your numeric Telegram user id from @userinfobot |
| `MAIL_CONTAINER` | yes      | Name of the docker-mailserver container         |
| `MAIL_DOMAIN`    | yes      | Domain the mail server hosts                    |

Only `BOT_USER_ID` is allowed to issue commands — everyone else is ignored.

### A note on security

To manage aliases, the bot runs commands inside your mail container. Docker
commands go through a special file called the **Docker socket**, and access to
that socket is effectively full control of Docker on the machine. So it matters
who gets it:

- **With the socket proxy (recommended):** the bot talks to a small helper
  container that only lets it run commands inside your existing mail container.
  It cannot start, stop, or create containers.
- **Without the proxy (simplest):** the bot gets direct access to the Docker
  socket. If the bot is ever compromised, so is the whole server. One command to
  run, but only do this if you accept that risk.

Pick one of the following:

### Locally with the socket proxy (Docker Compose)

```sh
BOT_TOKEN=<token> \
BOT_USER_ID=<id> \
MAIL_CONTAINER=mail \
MAIL_DOMAIN=example.com \
  make up        # pulls the published image, then docker compose up -d
```

### Without the proxy (one-line `docker run`)

The bot gets the full Docker socket directly (see the security note above):

```sh
docker run -d \
  --name dms-admin-bot \
  --restart unless-stopped \
  -e BOT_TOKEN=<token> \
  -e BOT_USER_ID=<id> \
  -e MAIL_CONTAINER=mail \
  -e MAIL_DOMAIN=example.com \
  -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/dtlp/dms-admin-bot:latest
```

### Deploy to a remote mail server over SSH (with the proxy)

```sh
BOT_TOKEN=<token> \
BOT_USER_ID=<id> \
MAIL_CONTAINER=mail \
MAIL_DOMAIN=example.com \
  make deploy SSH_KEY=~/.ssh/my-key MAIL_HOST=mail.example.com
```

This copies `docker-compose.yaml` to the host, pulls the published image from
GHCR, and starts it. All four variables above are forwarded to the remote
`docker compose` commands. The host, SSH key and remote directory are
configurable Make variables (`SSH_USER`, `SSH_KEY`, `MAIL_HOST`, `REMOTE_DIR`).
