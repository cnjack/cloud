package domain

// ProvenanceActorRef is a bounded identity snapshot used only for display,
// audit and usage attribution. It must never participate in authorization.
type ProvenanceActorRef struct {
	Kind          string `json:"kind"`
	ID            string `json:"id,omitempty"`
	Label         string `json:"label"`
	Provider      string `json:"provider,omitempty"`
	ExternalID    string `json:"external_id,omitempty"`
	ExternalLabel string `json:"external_label,omitempty"`
}

// RunProvenanceSnapshot freezes identity facts that would otherwise disappear
// when a member, Automation or provider binding is renamed or deleted.
type RunProvenanceSnapshot struct {
	RequestedActor    *ProvenanceActorRef `json:"requested_actor,omitempty"`
	AccountableActor  *ProvenanceActorRef `json:"accountable_actor,omitempty"`
	AttributionSource string              `json:"attribution_source,omitempty"`
	Precision         string              `json:"precision,omitempty"`
	RuntimePrincipal  ProvenanceActorRef  `json:"runtime_principal"`
	WritebackActor    *ProvenanceActorRef `json:"writeback_actor,omitempty"`
}

func (s RunProvenanceSnapshot) Empty() bool {
	return s.RequestedActor == nil && s.AccountableActor == nil &&
		s.AttributionSource == "" && s.Precision == "" &&
		s.RuntimePrincipal.Kind == "" && s.WritebackActor == nil
}
