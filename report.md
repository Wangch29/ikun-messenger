# Ikun Messenger: A Distributed Instant Messaging System based on Raft

## 1. Project Goal

The primary goal of this project is to design and implement **Ikun Messenger**, a distributed, fault-tolerant Instant Messaging (IM) system. Unlike traditional centralized chat applications that rely on a single server—creating a single point of failure—Ikun Messenger is built upon the **Raft Consensus Algorithm** to ensure high availability and strong data consistency.

The project aims to achieve the following specific objectives:

*   **Raft Protocol Implementation:** To implement the core mechanics of Raft from scratch, including **Leader Election**, **Log Replication**, and **Heartbeat/Safety mechanisms**. The system should maintain a consistent state across a cluster of servers even in the presence of network delays or partitions.
*   **Fault Tolerance & High Availability:** To demonstrate that the chat service can remain operational even if a minority of servers (e.g., the Leader node) crash. The system must automatically detect failures, elect a new leader, and allow clients to reconnect seamlessly without data loss.
*   **Strong Consistency:** To ensure that the critical routing table (User ID -> Server Node mapping) is strongly consistent across all nodes. This guarantees that a message sent to a user is always routed to the correct server where the user is connected.
*   **Dual-Mode Messaging:**
    *   **Broadcast:** Real-time public channel communication.
    *   **Private Chat:** Unicast messaging using the distributed routing table for precise lookup and forwarding.
*   **User-Friendly TUI Client:** To provide a Terminal User Interface (TUI) client that abstracts the complexity of distributed systems, supporting automatic failover and reconnection.

---

## 2. Design and Implementation

The architecture follows a modular design, separating the consensus layer from the application logic.

### 2.1 System Design Graph

![System Architecture](./assets/singlenode.png)

### 2.2 System Architecture

The project is organized into four core layers:

1.  **Raft Module (`/raft`)**:
    *   Implements the Raft consensus algorithm as described in the extended Raft paper.
    *   **State Machine:** Manages states (Follower, Candidate, Leader) and transitions based on timeouts and RPCs.
    *   **Log Replication:** Handles `AppendEntries` RPC to replicate logs to followers.
    *   **Snapshotting:** Implements `InstallSnapshot` to compact logs and synchronize slow followers efficiently.
    *   **Persistence:** Uses a `MemoryStorage` (simulated persistence) to save state (`currentTerm`, `votedFor`, `log`) to survive crashes during runtime.

2.  **Key-Value Service (`/kvraft`)**:
    *   Acts as a fault-tolerant State Machine atop Raft.
    *   **Client Interaction:** Provides `Clerk` with `Get/Put` methods that automatically retry requests upon timeouts or leader changes.
    *   **Deduplication:** Maintains a `lastApplied` map to detect and ignore duplicate RPCs from clients, ensuring **exactly-once semantics**.
    *   **Snapshot Trigger:** Monitors Raft state size and triggers log compaction when it exceeds a threshold.

3.  **IM Server (`/im`)**:
    *   **Gateway:** Handles WebSocket connections from TUI clients.
    *   **Router (Distributed Hash Table):** Uses `kvraft` to store the location of every online user (`route:userID -> nodeAddress`).
    *   **Forwarding:** When User A sends a private message to User B:
        1.  Server queries `kvraft` for User B's location.
        2.  If User B is local, send directly via WebSocket.
        3.  If User B is on another node, forward the message via gRPC to that node.

4.  **TUI Client (`/cmd/tui.go`)**:
    *   Built with the **Bubble Tea** framework.
    *   **Auto-Discovery & Failover:** Reads `config.yaml` to find available servers. If a connection drops, it automatically polls other nodes to reconnect, ensuring user transparency.

### 2.3 Third-Party Libraries

I leveraged several robust open-source libraries to build a production-quality system:

**Core Dependencies:**

*   **gRPC** (`google.golang.org/grpc`): High-performance RPC framework used for all inter-node communication (Raft consensus, log replication, and message forwarding).
*   **Gin** (`github.com/gin-gonic/gin`): A fast HTTP web framework used to handle user login APIs and upgrade connections to WebSocket.
*   **Cobra** (`github.com/spf13/cobra`): A CLI library used to build the `ikun` command-line interface with subcommands (`server`, `tui`).
*   **Viper** (`github.com/spf13/viper`): Configuration management solution used to parse `config.yaml`.
*   **Gorilla WebSocket** (`github.com/gorilla/websocket`): The standard Go implementation of the WebSocket protocol, enabling real-time full-duplex communication between clients and the server.
*   **Bubble Tea** (`github.com/charmbracelet/bubbletea`): A functional TUI framework based on The Elm Architecture, used to build the interactive terminal client.
*   **Snowflake** (`github.com/bwmarrin/snowflake`): A decentralized unique ID generator used for request deduplication in the KV service.

**Helper Libraries:**

*   **Lipgloss** (`github.com/charmbracelet/lipgloss`): Used for styling and layout in the TUI client.
*   **Protobuf** (`google.golang.org/protobuf`): Data serialization protocol used by gRPC.

---

## 3. Assumptions and Justifications

### 3.1 Assumptions

1.  **Failure Model (Crash-Stop):**
    *   We assume a **fail-stop** model for all server nodes. This means when a server fails, it simply halts execution and does not send malicious or corrupted data to other nodes (i.e., we assume **No Byzantine Faults**).
    *   *Implication:* We do not implement complex cryptographic signatures to verify message integrity against malicious nodes, which is standard for Raft implementations.

2.  **Network Reliability:**
    *   We assume the network is **asynchronous and unreliable**. Messages (RPCs) may be delayed, dropped, or reordered.
    *   *Constraint:* However, we assume that network partitions are temporary. For the system to make progress (commit logs), a majority (quorum) of nodes must eventually be connected and reachable.

3.  **Static Cluster Membership:**
    *   We assume the cluster configuration (IP addresses and ports of the 3-5 nodes) is **static** and known at startup via `config.yaml`.
    *   *Limitation:* The system does not support dynamic membership changes (adding or removing servers at runtime) because implementing Raft Joint Consensus is beyond the scope of this project.

### 3.2 Design Justifications

1.  **Raft for Router Table Consistency (The Core Philosophy)**
    *   **Problem:** In traditional distributed IM systems, maintaining a consistent view of user locations is challenging. Consider a "Roaming" scenario: a user disconnects from Node A and reconnects to Node B (e.g., switching from Wi-Fi to 5G). In an eventually consistent system (like Gossip), different nodes might temporarily hold conflicting records of the user's location, leading to message loss or "split-brain" routing.
    *   **Solution:** We chose to treat the **Router Table** (mapping of `UserID -> NodeAddress`) as a Replicated State Machine managed by Raft.
    *   **Justification:** By offloading the complexity of consistency to the Raft layer, we ensure that every login/logout event is a committed log entry. This guarantees a **single source of truth** for user locations across the entire cluster. We sacrifice write latency (login speed) for absolute routing correctness, which is a worthwhile trade-off for a reliable messaging system.

2.  **Use of Go (Golang)**
    *   **Decision:** The system is implemented in Go.
    *   **Justification:** Go is the industry standard for cloud-native distributed systems (e.g., Kubernetes, Etcd). Its lightweight **Goroutines** allow us to easily manage thousands of concurrent WebSocket connections and Raft timers (ElectionTicker, HeartbeatSender) with minimal overhead compared to OS threads.

---

## 4. Evaluation

### Build

Enter my directory in Khoury cluster.

```sh 
[chengfengwang@linux-075 ikun-messenger]$ ls
api  cmd  config  config.yaml  go.mod  go.sum  im  kvraft  main.go  Makefile  raft  report.md
```

First, build grpc.

```sh 
make proto
```

Second, install go libs.

```sh
go mod tidy
```

You will see:

![gomodtidy](./assets/gotidy.png)

Now, build the executable:

```sh
go build -o ikun
```

You can see, "ikun" is the executable.

![executable](./assets/1.png)

### Start IM Server


#### Config

First, you should fill the config.yaml with your server addresses.

Only need to change the **host**, don't recommand to change the ports, id.

![2](./assets/2.png)

#### Start servers

We'll start three nodes in the example.

In your id=0 node (linux-075.khoury.northeastern.edu in my example), and I'll call it **node0** in the latter.

```sh
./ikun server -m 0
```

"server" means run the IM server 

"-m 0" means **it's the first node(id=0) in config.yaml.** You must run command in the correct server.

![3](./assets/3.png)

You will see server start runnning....

The same for **node1** and **node2**:

In node1 (linux-076.khoury.northeastern.edu)

```sh
./ikun server -m 1
```

In node2 (linux-077.khoury.northeastern.edu)

```sh
./ikun server -m 2
```

And you can see in one of your node that, a leader was elected.

![4](./assets/4.png)


#### Run Client and Global Channel

In a new server, for me, in linux-078.khoury.northeastern.edu, run 

```sh
./ikun tui
```

You'll see a place to enter your name, and I enter the name "ikun".

![5](./assets/5.png)

Press "Enter", you'll login to the IM client with username "ikun".

![](./assets/6.png)


In the new server, for me in linux-079.khoury.northeastern.edu, run another client:

```sh
./ikun tui
```

![](./assets/7.png)

But use a different username (I use "kobe"):

![](./assets/8.png)


And Now, you can chat in the global channel, and all users can receive your messages:


![](./assets/9.png)

![](./assets/10.png)

#### Private Channel

And if I(ikun) only want to talk to kobe, I can send a private message. 

First, press "ctrl+n" (you can see some help info at the bottom of the window too):

Enter the person you want to talk to, here I enter "kobe"

![](./assets/11.png)

Press "enter", and you can enter a private session to kobe, send something:

![](./assets/12.png)

And back to "kobe"'s window, you'll find a new session in the left sidebar.

Press "tab" to switch to the left sidebar, and use direction button to switch to the "ikun" session, then press "enter" to enter this session:

![](./assets/13.png)
![](./assets/14.png)

You can look at the bottom of your screen, there's key info.

And you can receive the private message from "ikun", and send back something!

![](./assets/15.png)



#### Server Crash

We'll explore "raft" features.

Now, both of "ikun" and "kobe" are connect to node0:

![](./assets/16.png)

Now we'll go to **node0**, and press "ctrl+c" to shutdown it.

![](./assets/17.png)

And both "ikun" and "kobe" are automated reconnected to node1:

![](./assets/18.png)

The red error just means node0 is disconnected, nothing to worry about...

And you can still talk when node0 is disconnected:

![](./assets/19.png)

#### Server rejoins

Now, restart node0, by running:

```sh
./ikun server -m 0
```

The node0 rejoin, and start the third client in a new server, for me, it's **linux-080.khoury.northeastern.edu**

Run tui and use the name "cxk"

```sh
./ikun tui
```

Send a private messages to "ikun" and "kobe",

And both of them can receive "cxk" message! (Remember to switch to cxk's session in the left sidebar when you check messages.)

![](./assets/20.png)
![](./assets/22.png)

Explain:

Clients "ikun" and "kobe" are connected to **Node 1**, while "cxk" is connected to **Node 0**.

![](./assets/23.png)

When **Node 0** restarts and rejoins the cluster, it automatically fetches logs from peers to rebuild its router table. As a result, Node 0 correctly identifies that "ikun" and "kobe" are on **Node 1** and successfully forwards private messages to them.

![](./assets/25.png)

This demonstrates Raft's consistency in action.


#### Network change

Raft maintains strong consistency, ensuring that for every user in the router table, there is exactly one specific node.

Press "ctrl+c" to exit the the third client "cxk" and login again, use the name of "ikun" to simulated node changing. 

```sh
./ikun tui # Then input the name "ikun"
```

Initially, "ikun" was connected to **Node 1**, but then re-connected to **Node 0**.

![](./assets/26.png)

Switch back to client "kobe"(the second window), and send some private message to ikun. 

When client "kobe" sends a message to "ikun", the message appears only in the new session (the third window). The old "ikun" session receives nothing.


![](./assets/27.png)


This demonstrates that the re-login event successfully updated the global router table via Raft, atomically changing the mapping for "ikun" from **Node 1** to **Node 0**. This verifies the **strong consistency** of the Raft implementation.

#### Raft tests

I have a test to only test the raft implementation,

```sh
go test -v -count=1 ./raft/
```

## 5. Achievements and underachievements.

### Achievements

*   **Core Raft:** Successfully implemented Leader Election, Log Replication, and Safety properties.
*   **Log Compaction:** Implemented Snapshot mechanism. The system can discard old logs and send Snapshots to lagging followers (`InstallSnapshot`).
*   **Linearizable KV Store:** Built a KV store that passes consistency checks even under partition/failure scenarios.
*   **Resilient Messaging:** Implemented a chat system where clients can continue communicating even if the server they were connected to crashes (Client-side failover + Server-side routing recovery).
*   **User-Friendly TUI Client:** Developed a fully interactive **Text-Based User Interface (TUI)**. Unlike a raw command-line interface that requires manual command entry, the TUI provides an intuitive visual layout for message history and navigation, significantly improving usability.


### Underachievements
*   **Joint Consensus:** Dynamic membership changes are not supported.
*   **Persistent Storage:** Use a k-v database (e.g., using `rocksdb` or `leveldb`) would be a future improvement.
*   **Lack of Advanced Optimization Features:** The current implementation focuses on correctness and safety. Advanced performance features typically found in industrial Raft implementations, such as **Pre-Vote mechanisms** and **Follower Reads**, are outside the scope of this project.




