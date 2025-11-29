package raft

type RaftApi interface {
	// Submit a new command to the Raft.
	// Return the index and term of the new entry, and true if the operation is successful.
	Start(command []byte) (index int, term int, isLeader bool)

	// Get the current term and if the node is leader.
	GetState() (term int, isLeader bool)

	// Kill current node.
	Kill()
}
