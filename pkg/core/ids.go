package core

type ProjectID string
type TicketID string
type ActorID string
type ArtifactID string
type EventID string

func (id ProjectID) String() string  { return string(id) }
func (id TicketID) String() string   { return string(id) }
func (id ActorID) String() string    { return string(id) }
func (id ArtifactID) String() string { return string(id) }
func (id EventID) String() string    { return string(id) }
