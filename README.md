# Health Checker Cluster

A distributed health checking and leader election system with Cloudflare DNS failover.

> [!WARNING]
> This is a proof of concept and should not be used in production.

## Features

- Distributed health checking between nodes
- Automatic leader election
- Cloudflare DNS failover
- Tests with 1-node, 2-node, and multi-node clusters

## Quick Start

1. Configure nodes in [config.yaml](config.yaml)
2. Build and run:

   ```bash
   make setup
   make start  # 3 nodes
   # or
   make start-single  # 1 node
   # or
   make start-duo     # 2 nodes
   # or
   make start-multi   # 5+ nodes
   ```