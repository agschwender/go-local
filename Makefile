default: help

help: ## Show this help
	@echo "Go Local Example"
	@echo "================"
	@echo
	@echo "Demonstration of running a basic go application"
	@echo
	@fgrep -h " ## " $(MAKEFILE_LIST) | fgrep -v fgrep | sed -Ee 's/([a-z.]*):[^#]*##(.*)/\1##\2/' | column -t -s "##"

build: running ## Rebuild the application
	@docker-compose exec app go install -v -buildvcs=false ./cmd/...

build-dependencies: running ## Install new dependencies
	@docker-compose exec app go mod tidy

clean: ## Stop and delete all data
	@docker-compose down --remove-orphans --volumes

lint: ## Lint the application
	@docker-compose run --rm lint
	@echo "Lint completed with no errors"

logs: running ## Show the application logs
	@docker-compose logs --follow --tail=1000 app

run: ## Run the application in the foreground
	@docker-compose up --build app

running:
	@if [ -z `docker-compose ps -q app` ]; then \
		echo "Application must be running, try running \"make start\" first"; \
		exit 1; \
	fi

running-db:
	@if [ -z `docker-compose ps -q app` ]; then \
		echo "Application must be running, try running \"make start\" first"; \
		exit 1; \
	fi

shell: running ## Create a shell in the application container
	@docker-compose exec app /bin/sh

shell-db: running-db ## Create shell to database
	docker-compose exec db /bin/sh -c "psql postgres://\$$POSTGRES_USER:\$$POSTGRES_PASSWORD@localhost:5432/\$$POSTGRES_DB"

start: ## Run the application in the background
	@docker-compose up --build -d app

stop: ## Stop the running application
	@docker-compose down --remove-orphans

test: ## Test the application
	@docker-compose run --rm app go test ./...
