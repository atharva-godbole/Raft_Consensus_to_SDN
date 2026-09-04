package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"

	"github.com/hashicorp/raft"
)

// CommandType distinguishes between network flow rules and standard data
type CommandType string

const (
	TypeFlow CommandType = "FLOW"
	TypeKV   CommandType = "KV"
)

// Command represents a replicated transaction in the Raft log
type Command struct {
	Type     CommandType `json:"type"`
	Op       string      `json:"op"`       // "SET", "DEL", "ADD"
	Key      string      `json:"key"`      // Used for KV
	Value    string      `json:"value"`    // Used for KV
	Match    string      `json:"match"`    // Used for OpenFlow (e.g., "ip,nw_dst=10.0.0.2")
	Action   string      `json:"action"`   // Used for OpenFlow (e.g., "output:2")
	Priority int         `json:"priority"` // OpenFlow rule priority
}

// FlowFSM implements raft.FSM
type FlowFSM struct {
	mu        sync.RWMutex
	FlowRules map[string]string // Match -> Action
	KVStore   map[string]string // Key -> Value
	SwitchTCP string            // Open vSwitch address, e.g. "127.0.0.1:6633"
	IsLeader  bool
}

// NewFlowFSM initializes an empty state machine
func NewFlowFSM(switchTCP string) *FlowFSM {
	return &FlowFSM{
		FlowRules: make(map[string]string),
		KVStore:   make(map[string]string),
		SwitchTCP: switchTCP,
	}
}

// Apply executes on every node once Raft confirms a log entry has reached quorum
func (f *FlowFSM) Apply(l *raft.Log) interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()

	var cmd Command
	if err := json.Unmarshal(l.Data, &cmd); err != nil {
		log.Printf("[FSM ERROR] Bad command format: %v", err)
		return err
	}

	switch cmd.Type {
	case TypeKV:
		if cmd.Op == "SET" {
			f.KVStore[cmd.Key] = cmd.Value
			log.Printf("[KV COMMIT] %s = %s", cmd.Key, cmd.Value)
		} else if cmd.Op == "DEL" {
			delete(f.KVStore, cmd.Key)
			log.Printf("[KV COMMIT] Deleted key: %s", cmd.Key)
		}

	case TypeFlow:
		if cmd.Op == "ADD" {
			f.FlowRules[cmd.Match] = cmd.Action
			log.Printf("[FLOW COMMIT] Added: %s -> %s", cmd.Match, cmd.Action)
			// Only push to Open vSwitch if this node is the active cluster leader
			if f.IsLeader {
				f.pushFlowToSwitch(cmd)
			}
		} else if cmd.Op == "DEL" {
			delete(f.FlowRules, cmd.Match)
			log.Printf("[FLOW COMMIT] Deleted: %s", cmd.Match)
			if f.IsLeader {
				f.deleteFlowFromSwitch(cmd)
			}
		}
	}

	return nil
}

// pushFlowToSwitch sends an OpenFlow rule to Open vSwitch over TCP
func (f *FlowFSM) pushFlowToSwitch(cmd Command) {
	rule := fmt.Sprintf("priority=%d,%s,actions=%s", cmd.Priority, cmd.Match, cmd.Action)
	target := fmt.Sprintf("tcp:%s", f.SwitchTCP)

	cmdExec := exec.Command("ovs-ofctl", "-O", "OpenFlow13", "add-flow", target, rule)
	out, err := cmdExec.CombinedOutput()
	if err != nil {
		log.Printf("[OVS ERROR] Failed to add flow (%s): %v | %s", rule, err, string(out))
		return
	}
	log.Printf("[OVS SUCCESS] Added flow -> %s", rule)
}

// deleteFlowFromSwitch removes matching OpenFlow rules from Open vSwitch
func (f *FlowFSM) deleteFlowFromSwitch(cmd Command) {
	target := fmt.Sprintf("tcp:%s", f.SwitchTCP)

	cmdExec := exec.Command("ovs-ofctl", "-O", "OpenFlow13", "del-flows", target, cmd.Match)
	out, err := cmdExec.CombinedOutput()
	if err != nil {
		log.Printf("[OVS ERROR] Failed to delete flow: %v | %s", err, string(out))
		return
	}
	log.Printf("[OVS SUCCESS] Deleted flow matching -> %s", cmd.Match)
}

// ReconcileAll syncs all known flow rules to the switch (called when this node becomes leader)
func (f *FlowFSM) ReconcileAll() {
	f.mu.RLock()
	defer f.mu.RUnlock()

	log.Printf("[RECONCILE] Pushing %d existing rules to switch...", len(f.FlowRules))
	for match, action := range f.FlowRules {
		cmd := Command{
			Priority: 100,
			Match:    match,
			Action:   action,
		}
		f.pushFlowToSwitch(cmd)
	}
}


// Snapshot dumps current in-memory rules to a snapshot object
func (f *FlowFSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// Clone the map so writes can continue safely
	clone := make(map[string]string)
	for k, v := range f.FlowRules {
		clone[k] = v
	}
	return &FlowSnapshot{rules: clone}, nil
}

// Restore resets the in-memory rules from a saved snapshot
func (f *FlowFSM) Restore(rc io.ReadCloser) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	defer rc.Close()

	return json.NewDecoder(rc).Decode(&f.FlowRules)
}

// FlowSnapshot satisfies the raft.FSMSnapshot interface
type FlowSnapshot struct {
	rules map[string]string
}

func (s *FlowSnapshot) Persist(sink raft.SnapshotSink) error {
	data, err := json.Marshal(s.rules)
	if err != nil {
		sink.Cancel()
		return err
	}
	if _, err := sink.Write(data); err != nil {
		sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *FlowSnapshot) Release() {}
