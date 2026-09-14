IMAGE := ghcr.io/dtlp/dms-admin-bot
TAG := latest

# Remote deployment over SSH. Override on the command line for your host, e.g.
# make deploy MAIL_HOST=mail.example.com SSH_KEY=~/.ssh/id_ed25519
SSH_USER ?= root
SSH_KEY ?= ~/.ssh/id_ed25519
MAIL_HOST ?= mail.example.com
REMOTE_DIR ?= /opt/dms-admin-bot

.PHONY: build test image up down logs deploy

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/dms-admin-bot .

test:
	go test ./...

image:
	docker build -t $(IMAGE):$(TAG) .

up:
	docker compose pull && docker compose up -d

down:
	docker compose down

logs:
	docker compose logs -f

deploy:
	@test -n "$$BOT_TOKEN" || (echo "BOT_TOKEN is required"; exit 1)
	@test -n "$$BOT_USER_ID" || (echo "BOT_USER_ID is required"; exit 1)
	@test -n "$$MAIL_CONTAINER" || (echo "MAIL_CONTAINER is required"; exit 1)
	@test -n "$$MAIL_DOMAIN" || (echo "MAIL_DOMAIN is required"; exit 1)
	ssh -i $(SSH_KEY) $(SSH_USER)@$(MAIL_HOST) mkdir -p $(REMOTE_DIR)
	scp -i $(SSH_KEY) docker-compose.yaml $(SSH_USER)@$(MAIL_HOST):$(REMOTE_DIR)/
	ssh -i $(SSH_KEY) $(SSH_USER)@$(MAIL_HOST) "cd $(REMOTE_DIR) && rm -f docker-compose.yml && export BOT_TOKEN='$$BOT_TOKEN' BOT_USER_ID='$$BOT_USER_ID' MAIL_CONTAINER='$$MAIL_CONTAINER' MAIL_DOMAIN='$$MAIL_DOMAIN' && docker compose -f docker-compose.yaml pull && docker compose -f docker-compose.yaml up -d"