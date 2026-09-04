package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

// SetupRaft initializes and boots an individual Raft node
func SetupRaft(nodeID, raftAddr, dataDir string, fsm *FlowFSM, bootstrap bool) (*raft.Raft, error) {
	// 1. Base Raft Configuration
	config := raft.DefaultConfig()
	config.LocalID = raft.ServerID(nodeID)
	// Faster timeouts for local testing
	config.HeartbeatTimeout = 250 * time.Millisecond
	config.ElectionTimeout = 250 * time.Millisecond
	config.CommitTimeout = 50 * time.Millisecond
	config.LeaderLeaseTimeout = 250 * time.Millisecond // <-- ADD THIS LINE

	// 2. Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create data dir: %v", err)
	}

	// 3. Setup TCP Transport for Raft inter-node messaging
	address, err := net.ResolveTCPAddr("tcp", raftAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve TCP addr: %v", err)
	}

	transport, err := raft.NewTCPTransport(raftAddr, address, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to create transport: %v", err)
	}

	// 4. Setup Snapshot Storage (keeps last 2 snapshots on disk)
	snapshots, err := raft.NewFileSnapshotStore(dataDir, 2, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot store: %v", err)
	}

	// 5. Setup BoltDB for Log and Stable metadata storage
	dbPath := filepath.Join(dataDir, "raft.db")
	boltStore, err := raftboltdb.NewBoltStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create bolt store: %v", err)
	}

	// 6. Instantiate the Raft Engine
	// We pass boltStore twice: once as LogStore, once as StableStore
	r, err := raft.NewRaft(config, fsm, boltStore, boltStore, snapshots, transport)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize raft: %v", err)
	}

	// 7. If this is the initial seed node, bootstrap the cluster
	if bootstrap {
		configuration := raft.Configuration{
			Servers: []raft.Server{
				{
					ID:      config.LocalID,
					Address: transport.LocalAddr(),
				},
			},
		}
		r.BootstrapCluster(configuration)
	}

	return r, nil
}
