package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/caarlos0/env/v10"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Cluster    ClusterConfig    `yaml:"cluster"`
	Cloudflare CloudflareConfig `yaml:"cloudflare,omitempty"`
	NodeIP     string           `env:"NODE_IP"`
}

type ClusterConfig struct {
	Nodes               []string      `yaml:"nodes"`
	PingInterval        time.Duration `yaml:"ping_interval"`
	HealthTimeout       time.Duration `yaml:"health_timeout"`
	FailureThreshold    int           `yaml:"failure_threshold"`
	LeaderCheckInterval time.Duration `yaml:"leader_check_interval"`
	EmergencyTimeout    time.Duration `yaml:"emergency_timeout"`
	UdpPort             int           `yaml:"udp_port"`
}

type CloudflareConfig struct {
	ApiKey  string   `yaml:"api_key" env:"CLOUDFLARE_API_KEY"`
	Email   string   `yaml:"email" env:"CLOUDFLARE_EMAIL"`
	ZoneID  string   `yaml:"zone_id" env:"CLOUDFLARE_ZONE_ID"`
	Domains []string `yaml:"domains" env:"CLOUDFLARE_DOMAINS" envSeparator:","`
}

type PingMessage struct {
	From      string    `json:"from"`
	Timestamp time.Time `json:"timestamp"`
}

type NodeStatus struct {
	LastSeen     time.Time
	IsHealthy    bool
	FailureCount int
}

type HealthChecker struct {
	config     Config
	myIP       string
	nodeStatus map[string]*NodeStatus
	statusMu   sync.RWMutex
	isLeader   bool
	leaderMu   sync.RWMutex
	udpConn    *net.UDPConn
	startTime  time.Time
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

type CloudflareDNSRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl,omitempty"`
	Proxied bool   `json:"proxied,omitempty"`
}

type CloudflareBatchRequest struct {
	Create []CloudflareDNSRecord `json:"create,omitempty"`
	Update []CloudflareDNSRecord `json:"update,omitempty"`
	Delete []string              `json:"delete,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: health-checker <config.yaml>")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hc, err := NewHealthChecker(ctx, os.Args[1])
	if err != nil {
		log.Fatalf("Failed to create health checker: %v", err)
	}
	defer hc.Close()

	if err := hc.Start(); err != nil {
		log.Fatalf("Failed to start health checker: %v", err)
	}

	log.Printf("Health checker started on %s (cluster size: %d)",
		hc.myIP, len(hc.config.Cluster.Nodes))

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigChan:
		log.Printf("Received signal %v, shutting down...", sig)
	case <-ctx.Done():
	}

	cancel()
	hc.wg.Wait()
	log.Println("Shutdown complete")
}

func NewHealthChecker(ctx context.Context, configFile string) (*HealthChecker, error) {
	config, err := loadConfig(configFile)
	if err != nil {
		return nil, fmt.Errorf("error loading config: %v", err)
	}

	if config.Cluster.PingInterval == 0 {
		return nil, fmt.Errorf("ping_interval must be specified in config")
	}
	if config.Cluster.HealthTimeout == 0 {
		return nil, fmt.Errorf("health_timeout must be specified in config")
	}
	if config.Cluster.FailureThreshold == 0 {
		return nil, fmt.Errorf("failure_threshold must be specified in config")
	}
	if config.Cluster.LeaderCheckInterval == 0 {
		return nil, fmt.Errorf("leader_check_interval must be specified in config")
	}
	if config.Cluster.EmergencyTimeout == 0 {
		return nil, fmt.Errorf("emergency_timeout must be specified in config")
	}
	if config.Cluster.UdpPort == 0 {
		return nil, fmt.Errorf("udp_port must be specified in config")
	}

	myIP, err := findMyIP(config.Cluster.Nodes, config.NodeIP)
	if err != nil {
		return nil, fmt.Errorf("error determining node IP: %v", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	hc := &HealthChecker{
		config:     config,
		myIP:       myIP,
		nodeStatus: make(map[string]*NodeStatus),
		startTime:  time.Now(),
		ctx:        ctx,
		cancel:     cancel,
	}

	hc.initializeNodeStatus()
	return hc, nil
}

func (hc *HealthChecker) Start() error {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", hc.config.Cluster.UdpPort))
	if err != nil {
		return fmt.Errorf("resolve UDP address: %w", err)
	}

	hc.udpConn, err = net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("listen UDP: %w", err)
	}

	hc.wg.Add(4)
	go hc.handlePings()
	go hc.sendPings()
	go hc.monitorHealth()
	go hc.electLeader()

	return nil
}

func (hc *HealthChecker) Close() error {
	hc.cancel()
	if hc.udpConn != nil {
		return hc.udpConn.Close()
	}
	return nil
}

func (hc *HealthChecker) handlePings() {
	defer hc.wg.Done()

	buffer := make([]byte, 1024)

	for {
		select {
		case <-hc.ctx.Done():
			return
		default:
		}

		hc.udpConn.SetReadDeadline(time.Now().Add(time.Second))
		n, addr, err := hc.udpConn.ReadFromUDP(buffer)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Printf("UDP read error: %v", err)
			continue
		}

		var ping PingMessage
		if err := json.Unmarshal(buffer[:n], &ping); err != nil {
			log.Printf("Invalid ping message: %v", err)
			continue
		}

		hc.handlePingMessage(ping, addr)
	}
}

func (hc *HealthChecker) handlePingMessage(ping PingMessage, addr *net.UDPAddr) {
	if !hc.isKnownNode(ping.From) {
		log.Printf("Ping from unknown node: %s", ping.From)
		return
	}

	hc.updateNodeHealth(ping.From, true)

	pong := PingMessage{
		From:      hc.myIP,
		Timestamp: time.Now(),
	}

	if data, err := json.Marshal(pong); err == nil {
		hc.udpConn.WriteToUDP(data, addr)
	}
}

func (hc *HealthChecker) sendPings() {
	defer hc.wg.Done()

	ticker := time.NewTicker(hc.config.Cluster.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			hc.pingAllNodes()
		case <-hc.ctx.Done():
			return
		}
	}
}

func (hc *HealthChecker) pingAllNodes() {
	ping := PingMessage{
		From:      hc.myIP,
		Timestamp: time.Now(),
	}

	data, err := json.Marshal(ping)
	if err != nil {
		log.Printf("Failed to marshal ping: %v", err)
		return
	}

	for _, nodeIP := range hc.config.Cluster.Nodes {
		if nodeIP == hc.myIP {
			continue
		}

		addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", nodeIP, hc.config.Cluster.UdpPort))
		if err != nil {
			log.Printf("Failed to resolve %s: %v", nodeIP, err)
			continue
		}

		if _, err := hc.udpConn.WriteToUDP(data, addr); err != nil {
			log.Printf("Failed to ping %s: %v", nodeIP, err)
		}
	}
}

func (hc *HealthChecker) monitorHealth() {
	defer hc.wg.Done()

	ticker := time.NewTicker(hc.config.Cluster.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-hc.ctx.Done():
			return
		case <-ticker.C:
			hc.checkNodeHealth()
		}
	}
}

func (hc *HealthChecker) checkNodeHealth() {
	hc.statusMu.Lock()
	defer hc.statusMu.Unlock()

	now := time.Now()
	dnsUpdateNeeded := false

	for nodeIP, status := range hc.nodeStatus {
		if nodeIP == hc.myIP {
			continue
		}

		if now.Sub(status.LastSeen) > hc.config.Cluster.HealthTimeout {
			status.FailureCount++

			if status.FailureCount >= hc.config.Cluster.FailureThreshold && status.IsHealthy {
				status.IsHealthy = false
				dnsUpdateNeeded = true
				log.Printf("Node %s marked as DOWN (failures: %d)", nodeIP, status.FailureCount)
			}
		}
	}

	if dnsUpdateNeeded && hc.IsLeader() {
		go hc.updateDNS()
	}
}

func (hc *HealthChecker) electLeader() {
	defer hc.wg.Done()

	ticker := time.NewTicker(hc.config.Cluster.LeaderCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			hc.performLeaderElection()
		case <-hc.ctx.Done():
			return
		}
	}
}

func (hc *HealthChecker) performLeaderElection() {
	hc.statusMu.RLock()
	now := time.Now()
	var healthyNodes []string

	// Always include self as healthy (this node is running)
	healthyNodes = append(healthyNodes, hc.myIP)

	// Check other nodes
	for ip, status := range hc.nodeStatus {
		if ip == hc.myIP {
			continue
		}

		if status.IsHealthy && now.Sub(status.LastSeen) < hc.config.Cluster.HealthTimeout {
			healthyNodes = append(healthyNodes, ip)
		}
	}
	hc.statusMu.RUnlock()

	totalNodes := len(hc.config.Cluster.Nodes)
	healthyCount := len(healthyNodes)

	// Sort to ensure consistent leader selection (only needed for 3+ nodes)
	sort.Strings(healthyNodes)
	candidateLeader := healthyNodes[0] // Lowest IP becomes leader

	shouldLead := hc.shouldBecomeLeader(totalNodes, healthyCount, candidateLeader)
	

	log.Printf("Leader election: healthy=%v (%d/%d), candidate=%s, should_lead=%t, am_leader=%t",
		healthyNodes, healthyCount, totalNodes, candidateLeader, shouldLead, hc.IsLeader())

	if shouldLead {
		hc.setLeader(true)
	} else {
		hc.setLeader(false)
	}
}

func (hc *HealthChecker) shouldBecomeLeader(total, healthy int, candidateLeader string) bool {
	if healthy == 0 {
		return false
	}

	switch total {
	case 1:
		// Single node: always lead
		return true

	case 2:
		// Two-node cluster: any healthy node can lead (no sorting/election needed)
		return healthy >= 1

	default:
		// 3+ nodes: use proper leader election with sorting
		// Only the candidate leader (lowest IP) can become leader
		if candidateLeader != hc.myIP {
			return false
		}

		// Need majority unless in emergency mode
		majority := (total / 2) + 1
		if healthy >= majority {
			return true
		}

		// Emergency mode: allow leadership with minority after timeout
		if hc.inEmergencyMode() {
			log.Printf("EMERGENCY MODE: Leading with minority (%d/%d nodes)", healthy, total)
			return true
		}

		return false
	}
}

func (hc *HealthChecker) inEmergencyMode() bool {
	return time.Since(hc.startTime) > hc.config.Cluster.EmergencyTimeout
}

func (hc *HealthChecker) updateDNS() {
	healthyNodes := hc.getHealthyNodes()

	log.Printf("Updating DNS records for domains %v with healthy nodes: %v",
		hc.config.Cloudflare.Domains, healthyNodes)

	if hc.config.Cloudflare.ApiKey == "" || hc.config.Cloudflare.Email == "" ||
		hc.config.Cloudflare.ZoneID == "" || len(hc.config.Cloudflare.Domains) == 0 {
		log.Println("Cloudflare configuration incomplete, skipping DNS update")
		return
	}

	if len(healthyNodes) == 0 {
		log.Println("No healthy nodes available, skipping DNS update")
		return
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	existingRecords, err := hc.getDNSRecords(client)
	if err != nil {
		log.Printf("Error getting existing DNS records: %v", err)
		return
	}

	batch := CloudflareBatchRequest{}

	for _, domain := range hc.config.Cloudflare.Domains {
		for _, nodeIP := range healthyNodes {
			recordName := fmt.Sprintf("%s.%s", nodeIP, domain)

			record := CloudflareDNSRecord{
				Type:    "A",
				Name:    recordName,
				Content: nodeIP,
				TTL:     1,
				Proxied: false,
			}

			if recordID, exists := existingRecords[recordName]; exists {
				record.ID = recordID
				batch.Update = append(batch.Update, record)
			} else {
				batch.Create = append(batch.Create, record)
			}
		}

		for recordName, recordID := range existingRecords {
			found := false
			for _, nodeIP := range healthyNodes {
				expectedName := fmt.Sprintf("%s.%s", nodeIP, domain)
				if recordName == expectedName {
					found = true
					break
				}
			}
			if !found {
				batch.Delete = append(batch.Delete, recordID)
			}
		}
	}

	if len(batch.Create) == 0 && len(batch.Update) == 0 && len(batch.Delete) == 0 {
		log.Println("No DNS changes detected")
		return
	}

	err = hc.executeBatchRequest(client, batch)
	if err != nil {
		log.Printf("Error executing batch DNS update: %v", err)
	}
}

func (hc *HealthChecker) getDNSRecords(client *http.Client) (map[string]string, error) {
	records := make(map[string]string)
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records?type=A",
		hc.config.Cloudflare.ZoneID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	hc.setAuthHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error fetching DNS records: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result struct {
		Success bool `json:"success"`
		Result  []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Content string `json:"content"`
		} `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("error decoding response: %v", err)
	}

	if !result.Success {
		return nil, fmt.Errorf("API request was not successful")
	}

	for _, record := range result.Result {
		records[record.Name] = record.ID
	}

	return records, nil
}

func (hc *HealthChecker) executeBatchRequest(client *http.Client, batch CloudflareBatchRequest) error {
	if len(batch.Create) == 0 && len(batch.Update) == 0 && len(batch.Delete) == 0 {
		return nil
	}

	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/batch",
		hc.config.Cloudflare.ZoneID)

	payload, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("error marshaling batch request: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("error creating batch request: %v", err)
	}

	hc.setAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error executing batch request: %v", err)
	}
	defer resp.Body.Close()

	var result struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("error decoding batch response: %v", err)
	}

	if !result.Success {
		errMsgs := make([]string, 0, len(result.Errors))
		for _, e := range result.Errors {
			errMsgs = append(errMsgs, fmt.Sprintf("Code %d: %s", e.Code, e.Message))
		}
		return fmt.Errorf("batch request failed: %s", strings.Join(errMsgs, "; "))
	}

	log.Printf("Successfully updated DNS records: %d created, %d updated, %d deleted",
		len(batch.Create), len(batch.Update), len(batch.Delete))
	return nil
}

func (hc *HealthChecker) setAuthHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+hc.config.Cloudflare.ApiKey)
	req.Header.Set("X-Auth-Email", hc.config.Cloudflare.Email)
	req.Header.Set("Content-Type", "application/json")
}

func (hc *HealthChecker) IsLeader() bool {
	hc.leaderMu.RLock()
	defer hc.leaderMu.RUnlock()
	return hc.isLeader
}

func (hc *HealthChecker) setLeader(leader bool) {
	hc.leaderMu.Lock()
	defer hc.leaderMu.Unlock()

	if leader != hc.isLeader {
		if leader {
			log.Printf("Became LEADER (healthy nodes: %v)", hc.getHealthyNodes())
		} else {
			log.Printf("Stepped down as LEADER")
		}
	}
	hc.isLeader = leader
}

func (hc *HealthChecker) getHealthyNodes() []string {
	hc.statusMu.RLock()
	defer hc.statusMu.RUnlock()

	var healthy []string
	for nodeIP, status := range hc.nodeStatus {
		if status.IsHealthy {
			healthy = append(healthy, nodeIP)
		}
	}
	return healthy
}

func (hc *HealthChecker) updateNodeHealth(nodeIP string, isHealthy bool) {
	hc.statusMu.Lock()
	defer hc.statusMu.Unlock()

	status, exists := hc.nodeStatus[nodeIP]
	if !exists {
		return
	}

	wasHealthy := status.IsHealthy
	status.LastSeen = time.Now()

	if isHealthy {
		status.IsHealthy = true
		status.FailureCount = 0

		if !wasHealthy {
			log.Printf("Node %s is back UP", nodeIP)
			if hc.IsLeader() {
				go hc.updateDNS()
			}
		}
	}
}

func (hc *HealthChecker) isKnownNode(ip string) bool {
	for _, nodeIP := range hc.config.Cluster.Nodes {
		if nodeIP == ip {
			return true
		}
	}
	return false
}

func (hc *HealthChecker) initializeNodeStatus() {
	for _, nodeIP := range hc.config.Cluster.Nodes {
		hc.nodeStatus[nodeIP] = &NodeStatus{
			LastSeen:  time.Now(),
			IsHealthy: nodeIP == hc.myIP, // Only self is initially healthy
		}
	}
}

func loadConfig(filename string) (Config, error) {
	var config Config

	if _, err := os.Stat(filename); err == nil {
		data, err := os.ReadFile(filename)
		if err != nil {
			return config, fmt.Errorf("error reading config file: %v", err)
		}

		if err := yaml.Unmarshal(data, &config); err != nil {
			return config, fmt.Errorf("error parsing config file: %v", err)
		}
	}

	if err := env.Parse(&config); err != nil {
		return config, fmt.Errorf("error parsing environment variables: %v", err)
	}

	if err := validateConfig(config); err != nil {
		return config, fmt.Errorf("invalid config: %v", err)
	}

	return config, nil
}

func validateConfig(config Config) error {
	if len(config.Cluster.Nodes) == 0 {
		return fmt.Errorf("no nodes specified in cluster")
	}

	for _, nodeIP := range config.Cluster.Nodes {
		if net.ParseIP(nodeIP) == nil {
			return fmt.Errorf("invalid IP address: %s", nodeIP)
		}
	}

	return nil
}

func findMyIP(nodeIPs []string, envNodeIP string) (string, error) {
	// First try the environment variable if provided
	if envNodeIP != "" {
		for _, nodeIP := range nodeIPs {
			if nodeIP == envNodeIP {
				return envNodeIP, nil
			}
		}
		return "", fmt.Errorf("NODE_IP %s not found in cluster nodes %v", envNodeIP, nodeIPs)
	}

	// Auto-detect IP from network interfaces
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("get interfaces: %w", err)
	}

	localIPs := make(map[string]bool)

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ipnet.IP.To4() != nil {
					localIPs[ipnet.IP.String()] = true
				}
			}
		}
	}

	for _, nodeIP := range nodeIPs {
		if localIPs[nodeIP] {
			return nodeIP, nil
		}
	}

	return "", fmt.Errorf("no matching IP found in cluster nodes %v (local IPs: %v)", nodeIPs, getKeys(localIPs))
}

func getKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
