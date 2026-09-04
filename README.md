# Single Raft-SDN Simulator

A fault-tolerant distributed Key-Value store (Country:Capital) integrated with an Open vSwitch (OVS) Software-Defined Networking (SDN) datapath using HashiCorp Raft.

## Architecture

![image](https://github.com/atharva-godbole/Raft_Consensus_to_SDN/blob/main/Explainer/single-raft-sdn-docs/Architecture_for_Simulator.png)

* **Consensus Plane**: Nodes (`n1`, `n2`, `n3`) form a single Raft cluster replicating application data across BoltDB storage.
* **Control Plane**: The active leader dynamically programs OpenFlow 1.3 rules into the virtual switch (`s1`).
* **Data Plane**: An external client (`h_client`) communicates exclusively with a Virtual IP (`10.0.0.100:8000`). The switch performs Layer 4 NAT to route traffic directly to the elected leader.

## Interactive CLI

Run `sudo python3 topo.py` to compile the Go binary, launch Mininet, and enter the custom CLI:

* `status [node]` — Inspect cluster health, node roles, and current KV state.
* `read <Country>` — Query a capital through the VIP (`10.0.0.100`).
* `write <Country> <Capital>` — Commit a new entry to the cluster via the VIP.
* `kill <n1|n2|n3>` — Safely kill a node to test leader election and switch reprogramming.
* `revive <n1|n2|n3>` — Bring a dead node back online to test state synchronization.

For detailed operational manuals, setup and manual testing procedures, see the `Explainer/` folder.
