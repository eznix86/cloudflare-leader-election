# Health Checker Cluster Management Makefile

.PHONY: help setup start start-single start-duo start-multi stop logs logs-node status kill revive network-partition restore-network partial-partition clean monitor build restart tail ps exec test-failover test-split-brain

# Default target
help: ## Show this help message
	@echo "Health Checker Test Commands"
	@echo ""
	@echo "Usage: make <target> [ARGS]"
	@echo ""
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'
	@echo ""
	@echo "Examples:"
	@echo "  make setup               - First time setup"
	@echo "  make start               - Start default 3-node cluster"
	@echo "  make start-duo           - Start 2-node cluster"
	@echo "  make start-single        - Start single-node cluster"
	@echo "  make kill NODE=2         - Kill node 2 to test failover"
	@echo "  make logs-node NODE=1    - View logs for node 1 only"
	@echo "  make network-partition   - Isolate node 1 (network split-brain test)"
	@echo "  make restore-network     - Reconnect node 1"
	@echo "  make monitor            - Watch cluster status in real-time"

setup: ## Initial environment setup
	@echo "Setting up health checker development environment..."
	@if [ -f go.sum ]; then \
		echo "Removing existing go.sum file..."; \
		rm go.sum; \
	fi
	@if [ -f go.mod ]; then \
		echo "Cleaning and downloading dependencies..."; \
		go clean -modcache; \
		go mod tidy; \
	fi
	@echo "Setup complete! Run: make start"

start: ## Start default 3-node cluster
	@echo "Starting health checker cluster (3 nodes)..."
	@docker-compose up --build -d
	@echo "Cluster started! View logs with: make logs"

start-single: ## Start single-node cluster
	@echo "Starting health checker cluster (1 node)..."
	@docker-compose -f docker-compose-single.yaml up --build -d
	@echo "Single-node cluster started! View logs with: make logs"

start-duo: ## Start 2-node cluster
	@echo "Starting health checker cluster (2 nodes)..."
	@docker-compose -f docker-compose-duo.yaml up --build -d
	@echo "Duo cluster started! View logs with: make logs"

start-multi: ## Start multi-node cluster
	@echo "Starting health checker cluster (multi-node)..."
	@docker-compose -f docker-compose-multi.yaml up --build -d
	@echo "Multi-node cluster started! View logs with: make logs"

stop: ## Stop the cluster (detects which compose file is running)
	@echo "Stopping health checker cluster..."
	@if docker-compose -f docker-compose-single.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-single.yaml down; \
	elif docker-compose -f docker-compose-duo.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-duo.yaml down; \
	elif docker-compose -f docker-compose-multi.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-multi.yaml down; \
	else \
		docker-compose down; \
	fi

logs: ## View logs from all nodes (detects which compose file is running)
	@if docker-compose -f docker-compose-single.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-single.yaml logs -f; \
	elif docker-compose -f docker-compose-duo.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-duo.yaml logs -f; \
	elif docker-compose -f docker-compose-multi.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-multi.yaml logs -f; \
	else \
		docker-compose logs -f; \
	fi

logs-node: ## View logs for specific node (use NODE=<num>, e.g., make logs-node NODE=1)
	@if [ -z "$(NODE)" ]; then \
		echo "Usage: make logs-node NODE=<node_number>"; \
		echo "Example: make logs-node NODE=1"; \
		exit 1; \
	fi
	@if docker-compose -f docker-compose-single.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-single.yaml logs -f "health-node$(NODE)"; \
	elif docker-compose -f docker-compose-duo.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-duo.yaml logs -f "health-node$(NODE)"; \
	elif docker-compose -f docker-compose-multi.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-multi.yaml logs -f "health-node$(NODE)"; \
	else \
		docker-compose logs -f "health-node$(NODE)"; \
	fi

status: ## Show cluster and leader status (detects which compose file is running)
	@echo "=== Container Status ==="
	@if docker-compose -f docker-compose-single.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-single.yaml ps; \
		COMPOSE_FILE="docker-compose-single.yaml"; \
	elif docker-compose -f docker-compose-duo.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-duo.yaml ps; \
		COMPOSE_FILE="docker-compose-duo.yaml"; \
	elif docker-compose -f docker-compose-multi.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-multi.yaml ps; \
		COMPOSE_FILE="docker-compose-multi.yaml"; \
	else \
		docker-compose ps; \
		COMPOSE_FILE="docker-compose.yaml"; \
	fi
	@echo ""
	@echo "=== Leader Status ==="
	@if docker-compose -f docker-compose-single.yaml ps -q 2>/dev/null | grep -q .; then \
		for i in 1; do \
			echo -n "Node $$i: "; \
			docker-compose -f docker-compose-single.yaml logs --tail 5 "health-node$$i" 2>/dev/null | grep -E "(LEADER|leader)" | tail -1 || echo "No leader logs"; \
		done; \
	elif docker-compose -f docker-compose-duo.yaml ps -q 2>/dev/null | grep -q .; then \
		for i in 1 2; do \
			echo -n "Node $$i: "; \
			docker-compose -f docker-compose-duo.yaml logs --tail 5 "health-node$$i" 2>/dev/null | grep -E "(LEADER|leader)" | tail -1 || echo "No leader logs"; \
		done; \
	elif docker-compose -f docker-compose-multi.yaml ps -q 2>/dev/null | grep -q .; then \
		for i in 1 2 3 4 5; do \
			if docker-compose -f docker-compose-multi.yaml ps "health-node$$i" 2>/dev/null | grep -q "health-node$$i"; then \
				echo -n "Node $$i: "; \
				docker-compose -f docker-compose-multi.yaml logs --tail 5 "health-node$$i" 2>/dev/null | grep -E "(LEADER|leader)" | tail -1 || echo "No leader logs"; \
			fi; \
		done; \
	else \
		for i in 1 2 3; do \
			echo -n "Node $$i: "; \
			docker-compose logs --tail 5 "health-node$$i" 2>/dev/null | grep -E "(LEADER|leader)" | tail -1 || echo "No leader logs"; \
		done; \
	fi

kill: ## Kill a specific node to test failover (use NODE=<num>, e.g., make kill NODE=2)
	@if [ -z "$(NODE)" ]; then \
		echo "Usage: make kill NODE=<node_number>"; \
		echo "Example: make kill NODE=2"; \
		exit 1; \
	fi
	@echo "Killing node $(NODE) to test failover..."
	@if docker-compose -f docker-compose-single.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-single.yaml stop "health-node$(NODE)"; \
	elif docker-compose -f docker-compose-duo.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-duo.yaml stop "health-node$(NODE)"; \
	elif docker-compose -f docker-compose-multi.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-multi.yaml stop "health-node$(NODE)"; \
	else \
		docker-compose stop "health-node$(NODE)"; \
	fi
	@echo "Node $(NODE) stopped. Watch logs with: make logs"

revive: ## Restart a killed node (use NODE=<num>, e.g., make revive NODE=2)
	@if [ -z "$(NODE)" ]; then \
		echo "Usage: make revive NODE=<node_number>"; \
		echo "Example: make revive NODE=2"; \
		exit 1; \
	fi
	@echo "Reviving node $(NODE)..."
	@if docker-compose -f docker-compose-single.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-single.yaml start "health-node$(NODE)"; \
	elif docker-compose -f docker-compose-duo.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-duo.yaml start "health-node$(NODE)"; \
	elif docker-compose -f docker-compose-multi.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-multi.yaml start "health-node$(NODE)"; \
	else \
		docker-compose start "health-node$(NODE)"; \
	fi
	@echo "Node $(NODE) started. Watch logs with: make logs-node NODE=$(NODE)"

network-partition: ## Create network partition (isolating node 1 from nodes 2&3)
	@echo "Creating network partition (isolating node 1 from nodes 2&3)..."
	@echo "Disconnecting node1 from the network..."
	@docker network disconnect ping_health-net health-checker-node1
	@echo "Node 1 is now isolated! It can't communicate with nodes 2&3"
	@echo "Watch the logs with: make logs"
	@echo "To restore: make restore-network"

restore-network: ## Restore network partition
	@echo "Restoring network partition..."
	@docker network connect ping_health-net health-checker-node1 --ip 172.20.0.10
	@echo "Node 1 reconnected to the network"

partial-partition: ## Information about creating partial partitions
	@echo "Creating partial partition (node 1 can't talk to node 2, but can talk to node 3)..."
	@echo "For partial partitions, use the 'kill' command to simulate node failures"
	@echo "Example: make kill NODE=2  # Simulates node 2 being unreachable"

clean: ## Clean up everything (containers, volumes, etc.)
	@echo "Cleaning up everything..."
	@docker-compose down -v 2>/dev/null || true
	@docker-compose -f docker-compose-single.yaml down -v 2>/dev/null || true
	@docker-compose -f docker-compose-duo.yaml down -v 2>/dev/null || true
	@docker-compose -f docker-compose-multi.yaml down -v 2>/dev/null || true
	@docker system prune -f
	@echo "Cleanup complete"

monitor: ## Real-time monitoring loop (Ctrl+C to stop)
	@echo "Starting monitoring loop (Ctrl+C to stop)..."
	@while true; do \
		clear; \
		date; \
		echo "=== Cluster Status ==="; \
		docker-compose ps | grep health-checker 2>/dev/null || \
		docker-compose -f docker-compose-single.yaml ps | grep health-checker 2>/dev/null || \
		docker-compose -f docker-compose-duo.yaml ps | grep health-checker 2>/dev/null || \
		docker-compose -f docker-compose-multi.yaml ps | grep health-checker 2>/dev/null || \
		echo "No running clusters found"; \
		echo ""; \
		echo "=== Recent Leader Changes ==="; \
		(docker-compose logs --tail 20 2>/dev/null || \
		 docker-compose -f docker-compose-single.yaml logs --tail 20 2>/dev/null || \
		 docker-compose -f docker-compose-duo.yaml logs --tail 20 2>/dev/null || \
		 docker-compose -f docker-compose-multi.yaml logs --tail 20 2>/dev/null) | \
		grep -E "(LEADER|leader|DOWN|UP)" | tail -5; \
		sleep 5; \
	done

# Additional utility targets
build: ## Build the application without starting
	@echo "Building health checker application..."
	@docker-compose build

restart: stop start ## Restart the entire cluster

tail: ## Tail logs with grep for important events
	@if docker-compose -f docker-compose-single.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-single.yaml logs -f | grep -E "(LEADER|leader|DOWN|UP|EMERGENCY|election)"; \
	elif docker-compose -f docker-compose-duo.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-duo.yaml logs -f | grep -E "(LEADER|leader|DOWN|UP|EMERGENCY|election)"; \
	elif docker-compose -f docker-compose-multi.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-multi.yaml logs -f | grep -E "(LEADER|leader|DOWN|UP|EMERGENCY|election)"; \
	else \
		docker-compose logs -f | grep -E "(LEADER|leader|DOWN|UP|EMERGENCY|election)"; \
	fi

ps: ## Show running containers
	@docker-compose ps 2>/dev/null || \
	 docker-compose -f docker-compose-single.yaml ps 2>/dev/null || \
	 docker-compose -f docker-compose-duo.yaml ps 2>/dev/null || \
	 docker-compose -f docker-compose-multi.yaml ps 2>/dev/null || \
	 echo "No running clusters found"

exec: ## Execute shell in a container (use NODE=<num>, e.g., make exec NODE=1)
	@if [ -z "$(NODE)" ]; then \
		echo "Usage: make exec NODE=<node_number>"; \
		echo "Example: make exec NODE=1"; \
		exit 1; \
	fi
	@if docker-compose -f docker-compose-single.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-single.yaml exec "health-node$(NODE)" /bin/sh; \
	elif docker-compose -f docker-compose-duo.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-duo.yaml exec "health-node$(NODE)" /bin/sh; \
	elif docker-compose -f docker-compose-multi.yaml ps -q 2>/dev/null | grep -q .; then \
		docker-compose -f docker-compose-multi.yaml exec "health-node$(NODE)" /bin/sh; \
	else \
		docker-compose exec "health-node$(NODE)" /bin/sh; \
	fi

test-failover: ## Run a complete failover test scenario
	@echo "=== Running Failover Test Scenario ==="
	@echo "1. Starting with all nodes healthy..."
	@make status
	@echo "2. Killing node 2 in 5 seconds..."
	@sleep 5
	@make kill NODE=2
	@echo "3. Waiting 30 seconds for failover..."
	@sleep 30
	@make status
	@echo "4. Reviving node 2..."
	@make revive NODE=2
	@echo "5. Waiting 20 seconds for recovery..."
	@sleep 20
	@make status
	@echo "=== Failover test complete ==="

test-split-brain: ## Run a split-brain test scenario
	@echo "=== Running Split-Brain Test Scenario ==="
	@echo "1. Starting with all nodes healthy..."
	@make status
	@echo "2. Creating network partition in 5 seconds..."
	@sleep 5
	@make network-partition
	@echo "3. Waiting 60 seconds for emergency mode..."
	@sleep 60
	@make status
	@echo "4. Restoring network..."
	@make restore-network
	@echo "5. Waiting 30 seconds for convergence..."
	@sleep 30
	@make status
	@echo "=== Split-brain test complete ==="