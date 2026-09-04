package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"time"

	"github.com/hashicorp/raft"
)

var (
	nodeIP       = flag.String("node-ip", "10.0.0.1", "Node Data-Plane IP")
	nodeMAC      = flag.String("node-mac", "00:00:00:00:00:01", "Node Data-Plane MAC")
	switchPort   = flag.String("switch-port", "1", "OVS port connecting this node")
	hostPort     = flag.String("host-port", "4", "OVS port connecting the client host")
	vip          = flag.String("vip", "10.0.0.100", "Cluster Service Virtual IP")
	switchTarget = flag.String("switch", "tcp:10.0.0.254:6633", "OVS switch address")
)

// claimSwitchLeadership overwrites VIP translation flows on switch s1
func claimSwitchLeadership() {
	log.Printf("[SDN CONTROL] Claiming datapath: directing %s to %s (Port %s)", *vip, *nodeIP, *switchPort)

	// Clean out existing VIP and return flows
	exec.Command("ovs-ofctl", "-O", "OpenFlow13", "del-flows", *switchTarget, fmt.Sprintf("ip,nw_dst=%s", *vip)).Run()
	exec.Command("ovs-ofctl", "-O", "OpenFlow13", "del-flows", *switchTarget, "tcp,tp_src=8000").Run()

	// Ingress NAT: rewrite VIP -> Leader IP/MAC and forward to Leader Port
	ingress := fmt.Sprintf("priority=200,ip,nw_dst=%s,tcp,tp_dst=8000,actions=mod_dl_dst:%s,mod_nw_dst:%s,output:%s",
		*vip, *nodeMAC, *nodeIP, *switchPort)
	if out, err := exec.Command("ovs-ofctl", "-O", "OpenFlow13", "add-flow", *switchTarget, ingress).CombinedOutput(); err != nil {
		log.Printf("[SDN ERROR] Ingress flow failed: %v | %s", err, string(out))
	}

	// Egress Reverse-NAT: rewrite Leader IP -> VIP so client accepts return packets
	egress := fmt.Sprintf("priority=200,ip,nw_src=%s,tcp,tp_src=8000,actions=mod_dl_src:00:00:00:00:00:ff,mod_nw_src:%s,output:%s",
		*nodeIP, *vip, *hostPort)
	if out, err := exec.Command("ovs-ofctl", "-O", "OpenFlow13", "add-flow", *switchTarget, egress).CombinedOutput(); err != nil {
		log.Printf("[SDN ERROR] Egress flow failed: %v | %s", err, string(out))
	}

	log.Println("[SDN CONTROL] OpenFlow rules active on switch.")
}

func main() {
	var (
		nodeID    = flag.String("id", "n1", "Node identifier")
		httpAddr  = flag.String("http", "0.0.0.0:8000", "Application HTTP bind address")
		raftPort  = flag.String("raft-port", "9001", "Raft TCP port")
		dataDir   = flag.String("data", "data/n1", "BoltDB storage path")
		bootstrap = flag.Bool("bootstrap", false, "Bootstrap cluster seed")
	)
	flag.Parse()

	raftAddr := fmt.Sprintf("%s:%s", *nodeIP, *raftPort)
	log.Printf("[BOOT] %s running (Data IP: %s, Raft: %s)", *nodeID, *nodeIP, raftAddr)

	fsm := NewFlowFSM()
	r, err := SetupRaft(*nodeID, raftAddr, *dataDir, fsm, *bootstrap)
	if err != nil {
		log.Fatalf("[FATAL] Raft startup failure: %v", err)
	}

	// Dynamic SDN Control Hook
	go func() {
		for isLeader := range r.LeaderCh() {
			fsm.mu.Lock()
			fsm.IsLeader = isLeader
			fsm.mu.Unlock()

			if isLeader {
				log.Println("[LEADERSHIP] Promoted to LEADER.")
				claimSwitchLeadership()
			} else {
				log.Println("[LEADERSHIP] Operating as FOLLOWER.")
			}
		}
	}()

	// Membership API
	http.HandleFunc("/join", func(w http.ResponseWriter, req *http.Request) {
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
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write([]byte(fmt.Sprintf("Joined %s\n", joinReq.NodeID)))
	})

	// Country:Capital Database API
	http.HandleFunc("/data", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			country := req.URL.Query().Get("country")
			capital, exists := fsm.Get(country)
			if !exists {
				http.Error(w, "Not found", http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"country": country, "capital": capital})

		case http.MethodPost:
			var reqData struct {
				Country string `json:"country"`
				Capital string `json:"capital"`
			}
			if err := json.NewDecoder(req.Body).Decode(&reqData); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			cmd, _ := json.Marshal(Command{Op: "SET", Key: reqData.Country, Value: reqData.Capital})
			if err := r.Apply(cmd, 2*time.Second).Error(); err != nil {
				http.Error(w, fmt.Sprintf("Apply error: %v", err), http.StatusConflict)
				return
			}
			w.Write([]byte(fmt.Sprintf("Stored: %s -> %s\n", reqData.Country, reqData.Capital)))
		}
	})

	// Cluster Status
	http.HandleFunc("/status", func(w http.ResponseWriter, req *http.Request) {
		fsm.mu.RLock()
		defer fsm.mu.RUnlock()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"node_id":   *nodeID,
			"is_leader": fsm.IsLeader,
			"leader":    r.Leader(),
			"data":      fsm.KVStore,
		})
	})

	log.Fatal(http.ListenAndServe(*httpAddr, nil))
}
