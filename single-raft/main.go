package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/hashicorp/raft"
)

func main() {
	// 1. Command-line flags
	var (
		nodeID    = flag.String("id", "node1", "Unique node identifier")
		httpAddr  = flag.String("http", "127.0.0.1:8001", "HTTP API listen address")
		raftAddr  = flag.String("raft", "127.0.0.1:9001", "Raft internal transport address")
		switchTCP = flag.String("switch", "127.0.0.1:6633", "Open vSwitch TCP address")
		dataDir   = flag.String("data", "data/node1", "Directory for BoltDB log storage")
		bootstrap = flag.Bool("bootstrap", false, "Bootstrap as the initial cluster seed")
	)
	flag.Parse()

	log.Printf("[STARTUP] Initializing %s (Raft: %s, HTTP: %s)...", *nodeID, *raftAddr, *httpAddr)

	// 2. Initialize FSM and Raft Engine
	fsm := NewFlowFSM(*switchTCP)
	r, err := SetupRaft(*nodeID, *raftAddr, *dataDir, fsm, *bootstrap)
	if err != nil {
		log.Fatalf("[FATAL] Failed to start Raft: %v", err)
	}

	// 3. Monitor Leadership Transitions
	go func() {
		for isLeader := range r.LeaderCh() {
			fsm.mu.Lock()
			fsm.IsLeader = isLeader
			fsm.mu.Unlock()

			if isLeader {
				log.Println("[LEADERSHIP] Promoted to LEADER. Reconciling datapath...")
				fsm.ReconcileAll()
			} else {
				log.Println("[LEADERSHIP] Demoted to FOLLOWER. Suppressing OVS writes.")
			}
		}
	}()

	// 4. HTTP API: Join peer nodes into the cluster
	http.HandleFunc("/join", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var joinReq struct {
			NodeID   string `json:"node_id"`
			RaftAddr string `json:"raft_addr"`
		}
		if err := json.NewDecoder(req.Body).Decode(&joinReq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		future := r.AddVoter(raft.ServerID(joinReq.NodeID), raft.ServerAddress(joinReq.RaftAddr), 0, 0)
		if err := future.Error(); err != nil {
			http.Error(w, fmt.Sprintf("Failed to add node: %v", err), http.StatusInternalServerError)
			return
		}

		log.Printf("[MEMBERSHIP] Joined node %s (%s) to cluster", joinReq.NodeID, joinReq.RaftAddr)
		w.Write([]byte(fmt.Sprintf("Node %s joined successfully\n", joinReq.NodeID)))
	})

	// 5. HTTP API: Submit OpenFlow routing rules
	http.HandleFunc("/flow", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var cmd Command
		if err := json.NewDecoder(req.Body).Decode(&cmd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cmd.Type = TypeFlow
		if cmd.Priority == 0 {
			cmd.Priority = 100
		}

		data, err := json.Marshal(cmd)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Propose command to Raft log
		applyFuture := r.Apply(data, 500*time.Millisecond)
		if err := applyFuture.Error(); err != nil {
			http.Error(w, fmt.Sprintf("Replication failed: %v", err), http.StatusInternalServerError)
			return
		}

		w.Write([]byte("Flow rule replicated and committed\n"))
	})

	// 6. HTTP API: Inspect node state
	http.HandleFunc("/status", func(w http.ResponseWriter, req *http.Request) {
		fsm.mu.RLock()
		defer fsm.mu.RUnlock()

		status := map[string]interface{}{
			"node_id":   *nodeID,
			"raft_addr": *raftAddr,
			"leader":    r.Leader(),
			"is_leader": fsm.IsLeader,
			"rules":     fsm.FlowRules,
			"kv":        fsm.KVStore,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	})

	// 7. Start HTTP Server
	log.Printf("[HTTP] Control plane listening on http://%s", *httpAddr)
	if err := http.ListenAndServe(*httpAddr, nil); err != nil {
		log.Fatalf("[FATAL] HTTP server failed: %v", err)
	}
}
