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

func SetupRaft(nodeID, raftAddr, dataDir string, fsm *FlowFSM, bootstrap bool) (*raft.Raft, error) {
	config := raft.DefaultConfig()
	config.LocalID = raft.ServerID(nodeID)
	config.HeartbeatTimeout = 250 * time.Millisecond
	config.ElectionTimeout = 250 * time.Millisecond
	config.CommitTimeout = 50 * time.Millisecond
	config.LeaderLeaseTimeout = 250 * time.Millisecond

	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create data dir: %v", err)
	}

	address, err := net.ResolveTCPAddr("tcp", raftAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve TCP addr: %v", err)
	}

	transport, err := raft.NewTCPTransport(raftAddr, address, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to create transport: %v", err)
	}

	snapshots, err := raft.NewFileSnapshotStore(dataDir, 2, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot store: %v", err)
	}

	dbPath := filepath.Join(dataDir, "raft.db")
	boltStore, err := raftboltdb.NewBoltStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create bolt store: %v", err)
	}

	r, err := raft.NewRaft(config, fsm, boltStore, boltStore, snapshots, transport)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize raft: %v", err)
	}

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
