# Health Checker Cluster

A distributed health checking and leader election system with Cloudflare DNS failover.

> [!WARNING]
> This is a proof of concept and should not be used in production.

## Idea

Imagine you have a web application on 3 VPS on Kubernetes. Ingress is used to route traffic to your application, but it is not enough to ensure that your application is always available.

When someone tries to access your application, it can go to one of the 3 VPS. If one VPS fails, Cloudflare DNS doesn't know about it, and even if you have multiple IPs for an A record, it will continue to serve traffic to the failed VPS.

One option is to use Cloudflare load balancer, but that costs money. Another option is to use this system. Each node will health check the others and elect a leader. The leader will update Cloudflare DNS to only include the healthy nodes.

This system helps you with:

- Distributed health checking between nodes
- Automatic leader election with different strategies based on cluster size
- Cloudflare DNS failover with automatic DNS updates
- Support for 1-node, 2-node, and multi-node clusters

## Leader Election Strategy

The system uses different leader election strategies based on cluster size:

- **1 node**: Always becomes leader (no election needed)
- **2 nodes**: Any healthy node can become leader independently 
- **3+ nodes**: Deterministic leader election using lowest IP address with majority consensus

This prevents split-brain scenarios while ensuring high availability across different cluster configurations.

## Quick Start

1. Configure nodes in [config.yaml](config.yaml)
2. Build and run:

   ```bash
   make setup
   make start        # 3 nodes
   # or
   make start-single # 1 node
   # or  
   make start-duo    # 2 nodes
   # or
   make start-multi  # 5+ nodes
   ```

## Configuration

Example `config.yaml`:

```yaml
cluster:
  nodes:
    - "10.0.0.1"
    - "10.0.0.2" 
    - "10.0.0.3"
  ping_interval: 5s
  health_timeout: 15s
  failure_threshold: 3
  leader_check_interval: 10s
  emergency_timeout: 300s
  udp_port: 8080

# You can use environment variables instead of the config file for cloudflare
cloudflare:
  api_key: "your_api_key"
  email: "your@email.com"
  zone_id: "your_zone_id"
  domains:
    - "example.com"
```

## How It Works

1. **Health Checking**: Nodes continuously ping each other via UDP
2. **Leader Election**: Healthy nodes participate in leader election based on cluster size
3. **DNS Updates**: The leader updates Cloudflare DNS records to reflect healthy nodes only
4. **Failover**: When nodes fail, DNS is automatically updated to route traffic only to healthy nodes

## Emergency Mode

In clusters with 3+ nodes, if the cluster operates with a minority of nodes for longer than the `emergency_timeout`, the remaining nodes can still elect a leader to maintain service availability.


## How to deploy this on Kubernetes (Not tested)

- Create a daemonset to run the health checker on each node
- Use environment variables with secrets and the configMap to store the configuration
- Enable hostNetwork to allow UDP communication and to get the node IP
- Let it do the work.