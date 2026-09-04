#!/usr/bin/env python3
import os
import signal
import time
from mininet.net import Mininet
from mininet.node import OVSSwitch
from mininet.cli import CLI

NODE_CONFIG = {
    'n1': {'ip': '10.0.0.1', 'mac': '00:00:00:00:00:01', 'port': '1'},
    'n2': {'ip': '10.0.0.2', 'mac': '00:00:00:00:00:02', 'port': '2'},
    'n3': {'ip': '10.0.0.3', 'mac': '00:00:00:00:00:03', 'port': '3'},
}

def is_pid_running(pid):
    try:
        os.kill(pid, 0)
        return True
    except (OSError, ProcessLookupError):
        return False

def get_node_pid(node_name):
    pid_file = f"data/{node_name}.pid"
    if not os.path.exists(pid_file):
        return None
    try:
        with open(pid_file, 'r') as f:
            pid = int(f.read().strip())
        return pid if is_pid_running(pid) else None
    except Exception:
        return None

class ClusterCLI(CLI):
    prompt = 'raft-sdn> '

    def do_write(self, line):
        """Usage: write <Country> <Capital>"""
        args = line.strip().split(None, 1)
        if len(args) < 2:
            print("Usage: write <Country> <Capital>")
            return
        country, capital = args[0], args[1]
        h_client = self.mn.get('h_client')
        cmd = (f'curl -s -X POST http://10.0.0.100:8000/data '
               f'-H "Content-Type: application/json" '
               f'-d \'{{"country":"{country}","capital":"{capital}"}}\'')
        print(h_client.cmd(cmd).strip())

    def do_read(self, line):
        """Usage: read <Country>"""
        country = line.strip()
        if not country:
            print("Usage: read <Country>")
            return
        h_client = self.mn.get('h_client')
        cmd = f'curl -s http://10.0.0.100:8000/data?country={country}'
        print(h_client.cmd(cmd).strip())

    def do_status(self, line):
        """Usage: status [node_name] (e.g. status n1, or status for all)"""
        target_node = line.strip()
        nodes_to_check = [target_node] if target_node in NODE_CONFIG else list(NODE_CONFIG.keys())
        
        if target_node and target_node not in NODE_CONFIG:
            print(f"Unknown node: {target_node}. Valid: n1, n2, n3")
            return

        for name in nodes_to_check:
            pid = get_node_pid(name)
            if not pid:
                print(f"[{name}] STATUS: OFF (Killed)")
                continue

            node = self.mn.get(name)
            ip = NODE_CONFIG[name]['ip']
            out = node.cmd(f'curl -s --max-time 1 http://{ip}:8000/status').strip()
            print(f"[{name}] (PID {pid}): {out if out else 'Unreachable'}")

    def do_kill(self, line):
        """Usage: kill <n1|n2|n3>"""
        name = line.strip()
        if name not in NODE_CONFIG:
            print("Specify valid node: kill n1, kill n2, kill n3")
            return

        pid = get_node_pid(name)
        if not pid:
            print(f"[{name}] is already OFF")
            return

        os.kill(pid, signal.SIGKILL)
        pid_file = f"data/{name}.pid"
        if os.path.exists(pid_file):
            os.remove(pid_file)
        print(f"[{name}] Process (PID {pid}) killed successfully.")

    def do_revive(self, line):
        """Usage: revive <n1|n2|n3>"""
        name = line.strip()
        if name not in NODE_CONFIG:
            print("Specify valid node: revive n1, revive n2, revive n3")
            return

        if get_node_pid(name):
            print(f"[{name}] is already running.")
            return

        node = self.mn.get(name)
        cfg = NODE_CONFIG[name]
        cmd = (f'./single-raft-sdn -id={name} -node-ip={cfg["ip"]} -node-mac={cfg["mac"]} '
               f'-switch-port={cfg["port"]} -data=data/{name} -bootstrap=false '
               f'> data/{name}.log 2>&1 & echo $! > data/{name}.pid')
        node.cmd(cmd)
        time.sleep(0.5)
        new_pid = get_node_pid(name)
        print(f"[{name}] Revived successfully (New PID: {new_pid}).")

def run():
    print("=== 1. Cleaning Stale Files & Compiling ===")
    os.system("pkill -9 -f single-raft-sdn 2>/dev/null || true")
    os.system("rm -rf data/")
    os.system("mkdir -p data/n1 data/n2 data/n3")

    if os.system("go build -o single-raft-sdn .") != 0:
        print("[ERROR] Go compilation failed.")
        return

    print("=== 2. Launching Mininet Topology ===")
    net = Mininet(topo=None, build=False, ipBase='10.0.0.0/24')
    s1 = net.addSwitch('s1', cls=OVSSwitch, protocols='OpenFlow13')

    n1 = net.addHost('n1', ip='10.0.0.1/24', mac='00:00:00:00:00:01')
    n2 = net.addHost('n2', ip='10.0.0.2/24', mac='00:00:00:00:00:02')
    n3 = net.addHost('n3', ip='10.0.0.3/24', mac='00:00:00:00:00:03')
    h_client = net.addHost('h_client', ip='10.0.0.10/24', mac='00:00:00:00:00:10')

    net.addLink(n1, s1, port1=0, port2=1)
    net.addLink(n2, s1, port1=0, port2=2)
    net.addLink(n3, s1, port1=0, port2=3)
    net.addLink(h_client, s1, port1=0, port2=4)

    net.start()

    print("=== 3. Setting Up Switch Base Flows ===")
    s1.cmd('ifconfig s1 10.0.0.254 netmask 255.255.255.0')
    s1.cmd('ovs-vsctl set-controller s1 ptcp:6633')
    s1.cmd('ovs-ofctl -O OpenFlow13 del-flows s1')
    s1.cmd('ovs-ofctl -O OpenFlow13 add-flow s1 "priority=100,arp,actions=flood"')
    s1.cmd('ovs-ofctl -O OpenFlow13 add-flow s1 "priority=100,ip,nw_dst=10.0.0.1,actions=output:1"')
    s1.cmd('ovs-ofctl -O OpenFlow13 add-flow s1 "priority=100,ip,nw_dst=10.0.0.2,actions=output:2"')
    s1.cmd('ovs-ofctl -O OpenFlow13 add-flow s1 "priority=100,ip,nw_dst=10.0.0.3,actions=output:3"')
    s1.cmd('ovs-ofctl -O OpenFlow13 add-flow s1 "priority=100,ip,nw_dst=10.0.0.10,actions=output:4"')
    s1.cmd('ovs-ofctl -O OpenFlow13 add-flow s1 "priority=100,ip,nw_dst=10.0.0.254,actions=LOCAL"')

    h_client.cmd('arp -s 10.0.0.100 00:00:00:00:00:ff')

    print("=== 4. Launching Cluster Nodes ===")
    n1.cmd('./single-raft-sdn -id=n1 -node-ip=10.0.0.1 -node-mac=00:00:00:00:00:01 -switch-port=1 -data=data/n1 -bootstrap=true > data/n1.log 2>&1 & echo $! > data/n1.pid')
    time.sleep(1)
    n2.cmd('./single-raft-sdn -id=n2 -node-ip=10.0.0.2 -node-mac=00:00:00:00:00:02 -switch-port=2 -data=data/n2 -bootstrap=false > data/n2.log 2>&1 & echo $! > data/n2.pid')
    n3.cmd('./single-raft-sdn -id=n3 -node-ip=10.0.0.3 -node-mac=00:00:00:00:00:03 -switch-port=3 -data=data/n3 -bootstrap=false > data/n3.log 2>&1 & echo $! > data/n3.pid')
    time.sleep(1)

    print("=== 5. Establishing Raft Quorum ===")
    n1.cmd('curl -s -X POST http://10.0.0.1:8000/join -H "Content-Type: application/json" -d \'{"node_id":"n2","raft_addr":"10.0.0.2:9001"}\'')
    n1.cmd('curl -s -X POST http://10.0.0.1:8000/join -H "Content-Type: application/json" -d \'{"node_id":"n3","raft_addr":"10.0.0.3:9001"}\'')
    time.sleep(1)

    print("=== 6. Seeding Initial KV Data ===")
    n1.cmd('curl -s -X POST http://10.0.0.1:8000/data -H "Content-Type: application/json" -d \'{"country":"France","capital":"Paris"}\'')
    n1.cmd('curl -s -X POST http://10.0.0.1:8000/data -H "Content-Type: application/json" -d \'{"country":"Japan","capital":"Tokyo"}\'')

    print("\nCustom commands available: write, read, status, kill, revive\n")
    ClusterCLI(net)

    os.system("pkill -9 -f single-raft-sdn 2>/dev/null || true")
    net.stop()

if __name__ == '__main__':
    run()
