package beads

// SelectiveProbeLimit is the page size of the bounded first read that a store
// may issue for a ListQuery with Selective set. It is far above the number of
// beads an identity lookup (one session name, one alias, one agent) can match,
// so the unbounded repeat that follows a full page is a safety net rather than
// a normal path.
const SelectiveProbeLimit = 32
