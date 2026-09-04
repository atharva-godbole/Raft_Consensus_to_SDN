package main

import (
	"encoding/json"
	"io"
	"log"
	"sync"

	"github.com/hashicorp/raft"
)

type Command struct {
	Op    string `json:"op"`    // "SET" or "DEL"
	Key   string `json:"key"`   // Country
	Value string `json:"value"` // Capital
}

type FlowFSM struct {
	mu       sync.RWMutex
	KVStore  map[string]string // Country -> Capital
	IsLeader bool
}

func NewFlowFSM() *FlowFSM {
	return &FlowFSM{
		KVStore: make(map[string]string),
	}
}

func (f *FlowFSM) Get(country string) (string, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	capital, exists := f.KVStore[country]
	return capital, exists
}

func (f *FlowFSM) Apply(l *raft.Log) interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()

	var cmd Command
	if err := json.Unmarshal(l.Data, &cmd); err != nil {
		log.Printf("[FSM ERROR] Parse error: %v", err)
		return err
	}

	switch cmd.Op {
	case "SET":
		f.KVStore[cmd.Key] = cmd.Value
		log.Printf("[COMMIT] %s -> %s", cmd.Key, cmd.Value)
	case "DEL":
		delete(f.KVStore, cmd.Key)
		log.Printf("[COMMIT] Deleted: %s", cmd.Key)
	}
	return nil
}

type FlowSnapshot struct {
	store map[string]string
}

func (f *FlowFSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	clone := make(map[string]string, len(f.KVStore))
	for k, v := range f.KVStore {
		clone[k] = v
	}
	return &FlowSnapshot{store: clone}, nil
}

func (s *FlowSnapshot) Persist(sink raft.SnapshotSink) error {
	data, err := json.Marshal(s.store)
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

func (f *FlowFSM) Restore(rc io.ReadCloser) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	defer rc.Close()
	return json.NewDecoder(rc).Decode(&f.KVStore)
}
