.PHONY: up down logs rogue
up:
	./scripts/bootstrap.sh
down:
	docker compose down
logs:
	docker compose logs -f order-client ledger-server
rogue:
	docker compose run --no-deps --rm rogue-client
