#!/usr/bin/env bash
set -e

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
cd "$DIR"

echo "=== 1. Cleaning Up Previous State ==="
pkill -9 -f single-raft-sdn 2>/dev/null || true
rm -rf data/ node*.log
mkdir -p data/node1 data/node2 data/node3

# Ensure binary is compiled
if [ ! -f "single-raft-sdn" ]; then
    echo "Compiling single-raft-sdn..."
    go build -o single-raft-sdn .
fi

echo "=== 2. Configuring Open vSwitch Bridge ==="
if sudo ovs-vsctl br-exists s1 2>/dev/null; then
    sudo ovs-vsctl del-manager 2>/dev/null || true
    sudo ovs-vsctl set-controller s1 ptcp:6633
    echo "[OK] Bridge s1 configured to listen on ptcp:6633"
else
    echo "[WARN] Bridge s1 not found. Start Mininet in your other terminal:"
    echo "       sudo mn --topo single,2 --mac --switch ovsk,protocols=OpenFlow13 --controller none"
fi

echo "=== 3. Starting Raft Nodes ==="
# Node 1 (Bootstrap Seed / Initial Leader)
./single-raft-sdn -id=node1 -http=127.0.0.1:8001 -raft=127.0.0.1:9001 -data=data/node1 -bootstrap=true > node1.log 2>&1 &
echo "[STARTED] Node 1 (PID $!) -> logging to node1.log"

# Wait for Node 1 HTTP API to become ready
echo -n "Waiting for Node 1 to initialize..."
until curl -s http://127.0.0.1:8001/status >/dev/null 2>&1; do
    sleep 0.2
    echo -n "."
done
echo " Ready!"

# Node 2 & 3 (Followers)
./single-raft-sdn -id=node2 -http=127.0.0.1:8002 -raft=127.0.0.1:9002 -data=data/node2 -bootstrap=false > node2.log 2>&1 &
echo "[STARTED] Node 2 (PID $!) -> logging to node2.log"

./single-raft-sdn -id=node3 -http=127.0.0.1:8003 -raft=127.0.0.1:9003 -data=data/node3 -bootstrap=false > node3.log 2>&1 &
echo "[STARTED] Node 3 (PID $!) -> logging to node3.log"

sleep 1

echo "=== 4. Forming Raft Cluster ==="
curl -s -X POST http://127.0.0.1:8001/join \
  -H "Content-Type: application/json" \
  -d '{"node_id":"node2", "raft_addr":"127.0.0.1:9002"}'
echo "Node 2 join requested"

curl -s -X POST http://127.0.0.1:8001/join \
  -H "Content-Type: application/json" \
  -d '{"node_id":"node3", "raft_addr":"127.0.0.1:9003"}'
echo "Node 3 join requested"

sleep 1

echo "=== 5. Injecting OpenFlow Routing Rules ==="
# 1. ARP Resolution Rule
curl -s -X POST http://127.0.0.1:8001/flow \
  -H "Content-Type: application/json" \
  -d '{"op":"ADD", "match":"arp", "action":"flood", "priority":100}' >/dev/null
echo "[FLOW ADD] ARP -> FLOOD"

# 2. Forward to h1 (Port 1)
curl -s -X POST http://127.0.0.1:8001/flow \
  -H "Content-Type: application/json" \
  -d '{"op":"ADD", "match":"ip,nw_dst=10.0.0.1", "action":"output:1", "priority":100}' >/dev/null
echo "[FLOW ADD] dst=10.0.0.1 -> output:1"

# 3. Forward to h2 (Port 2)
curl -s -X POST http://127.0.0.1:8001/flow \
  -H "Content-Type: application/json" \
  -d '{"op":"ADD", "match":"ip,nw_dst=10.0.0.2", "action":"output:2", "priority":100}' >/dev/null
echo "[FLOW ADD] dst=10.0.0.2 -> output:2"

sleep 0.5

echo "=== 6. Verification ==="
echo "Cluster Status:"
curl -s http://127.0.0.1:8001/status
echo ""

if sudo ovs-vsctl br-exists s1 2>/dev/null; then
    echo -e "\nOVS Flows in s1:"
    sudo ovs-ofctl -O OpenFlow13 dump-flows s1
fi

echo -e "\nSetup complete. You can now test in Mininet: h1 ping -c 3 h2"
