package beads

import (
	"context"
	"encoding/json"
	"errors"
)

// NewNotifyingStore wraps backing so successful writes emit bead change
// notifications. Reads delegate directly to backing.
func NewNotifyingStore(backing Store, onChange func(eventType, beadID string, payload json.RawMessage)) Store {
	if backing == nil || onChange == nil {
		return backing
	}
	return &notifyingStore{backing: backing, onChange: onChange}
}

type notifyingStore struct {
	backing  Store
	onChange func(eventType, beadID string, payload json.RawMessage)
}

func (s *notifyingStore) Create(b Bead) (Bead, error) {
	created, err := s.backing.Create(b)
	if err != nil {
		return created, err
	}
	created = s.freshOr(created.ID, created)
	s.notify("bead.created", created)
	return created, nil
}

func (s *notifyingStore) Get(id string) (Bead, error) {
	return s.backing.Get(id)
}

func (s *notifyingStore) Update(id string, opts UpdateOpts) error {
	if err := s.backing.Update(id, opts); err != nil {
		return err
	}
	if fresh, err := s.freshWithDeps(id); err == nil {
		s.notify("bead.updated", fresh)
	}
	return nil
}

func (s *notifyingStore) Close(id string) error {
	if err := s.backing.Close(id); err != nil {
		return err
	}
	fresh := s.freshOr(id, Bead{ID: id, Status: "closed"})
	fresh.Status = "closed"
	s.notify("bead.closed", fresh)
	return nil
}

func (s *notifyingStore) Reopen(id string) error {
	if err := s.backing.Reopen(id); err != nil {
		return err
	}
	fresh := s.freshOr(id, Bead{ID: id, Status: "open"})
	fresh.Status = "open"
	s.notify("bead.updated", fresh)
	return nil
}

func (s *notifyingStore) CloseAll(ids []string, metadata map[string]string) (int, error) {
	n, err := s.backing.CloseAll(ids, metadata)
	if err != nil && n == 0 {
		return n, err
	}
	for _, id := range ids {
		fresh, getErr := s.backing.Get(id)
		if getErr != nil || fresh.Status != "closed" {
			continue
		}
		s.notify("bead.closed", fresh)
	}
	return n, err
}

func (s *notifyingStore) List(query ListQuery) ([]Bead, error) {
	return s.backing.List(query)
}

func (s *notifyingStore) ListOpen(status ...string) ([]Bead, error) {
	return s.backing.ListOpen(status...)
}

func (s *notifyingStore) Ready(query ...ReadyQuery) ([]Bead, error) {
	return s.backing.Ready(query...)
}

func (s *notifyingStore) Children(parentID string, opts ...QueryOpt) ([]Bead, error) {
	return s.backing.Children(parentID, opts...)
}

func (s *notifyingStore) ListByLabel(label string, limit int, opts ...QueryOpt) ([]Bead, error) {
	return s.backing.ListByLabel(label, limit, opts...)
}

func (s *notifyingStore) ListByAssignee(assignee, status string, limit int) ([]Bead, error) {
	return s.backing.ListByAssignee(assignee, status, limit)
}

func (s *notifyingStore) ListByMetadata(filters map[string]string, limit int, opts ...QueryOpt) ([]Bead, error) {
	return s.backing.ListByMetadata(filters, limit, opts...)
}

func (s *notifyingStore) SetMetadata(id, key, value string) error {
	if err := s.backing.SetMetadata(id, key, value); err != nil {
		return err
	}
	if fresh, err := s.freshWithDeps(id); err == nil {
		s.notify("bead.updated", fresh)
	}
	return nil
}

func (s *notifyingStore) SetMetadataBatch(id string, kvs map[string]string) error {
	if err := s.backing.SetMetadataBatch(id, kvs); err != nil {
		return err
	}
	if fresh, err := s.freshWithDeps(id); err == nil {
		s.notify("bead.updated", fresh)
	}
	return nil
}

func (s *notifyingStore) Tx(commitMsg string, fn func(Tx) error) error {
	if fn == nil {
		return errors.New("beads tx: nil callback")
	}
	tx := newNotifyingStoreTx()
	if err := s.backing.Tx(commitMsg, func(backingTx Tx) error {
		tx.backing = backingTx
		return fn(tx)
	}); err != nil {
		return err
	}
	for _, id := range tx.ids {
		fresh, err := s.freshWithDeps(id)
		if err != nil {
			if _, closed := tx.closed[id]; closed {
				s.notify("bead.closed", Bead{ID: id, Status: "closed"})
			}
			continue
		}
		if fresh.Status == "closed" {
			s.notify("bead.closed", fresh)
		} else {
			s.notify("bead.updated", fresh)
		}
	}
	return nil
}

func (s *notifyingStore) Delete(id string) error {
	deleted, haveDeleted := s.snapshotBeforeDelete(id)
	if err := s.backing.Delete(id); err != nil {
		return err
	}
	if haveDeleted {
		s.notify("bead.deleted", deleted)
	}
	return nil
}

func (s *notifyingStore) Ping() error {
	return s.backing.Ping()
}

func (s *notifyingStore) DepAdd(issueID, dependsOnID, depType string) error {
	if err := s.backing.DepAdd(issueID, dependsOnID, depType); err != nil {
		return err
	}
	if fresh, err := s.freshWithDeps(issueID); err == nil {
		s.notify("bead.updated", fresh)
	}
	return nil
}

func (s *notifyingStore) DepRemove(issueID, dependsOnID string) error {
	if err := s.backing.DepRemove(issueID, dependsOnID); err != nil {
		return err
	}
	if fresh, err := s.freshWithDeps(issueID); err == nil {
		s.notify("bead.updated", fresh)
	}
	return nil
}

func (s *notifyingStore) DepList(id, direction string) ([]Dep, error) {
	return s.backing.DepList(id, direction)
}

func (s *notifyingStore) WaitForParentProjection(ctx context.Context, id, oldParentID, newParentID string) error {
	waiter, ok := s.backing.(ParentProjectionWaiter)
	if !ok {
		return nil
	}
	return waiter.WaitForParentProjection(ctx, id, oldParentID, newParentID)
}

func (s *notifyingStore) IDPrefix() string {
	if prefixed, ok := s.backing.(interface{ IDPrefix() string }); ok {
		return prefixed.IDPrefix()
	}
	return ""
}

func (s *notifyingStore) CloseStore() error {
	closer, ok := s.backing.(interface{ CloseStore() error })
	if !ok {
		return nil
	}
	return closer.CloseStore()
}

func (s *notifyingStore) Backing() Store {
	return s.backing
}

func (s *notifyingStore) freshOr(id string, fallback Bead) Bead {
	fresh, err := s.freshWithDeps(id)
	if err != nil {
		return fallback
	}
	return fresh
}

func (s *notifyingStore) freshWithDeps(id string) (Bead, error) {
	fresh, err := s.backing.Get(id)
	if err != nil {
		return Bead{}, err
	}
	if deps, err := s.backing.DepList(id, "down"); err == nil {
		fresh.Dependencies = cloneDeps(deps)
		fresh.Needs = nil
	}
	return fresh, nil
}

func (s *notifyingStore) snapshotBeforeDelete(id string) (Bead, bool) {
	deleted, err := s.freshWithDeps(id)
	if err != nil {
		return Bead{}, false
	}
	return deleted, true
}

func (s *notifyingStore) notify(eventType string, b Bead) {
	if s.onChange == nil || b.ID == "" {
		return
	}
	payload, err := MarshalEventBead(b)
	if err != nil {
		return
	}
	s.onChange(eventType, b.ID, payload)
}

type notifyingStoreTx struct {
	backing Tx
	seen    map[string]struct{}
	closed  map[string]struct{}
	ids     []string
}

func newNotifyingStoreTx() *notifyingStoreTx {
	return &notifyingStoreTx{
		seen:   make(map[string]struct{}),
		closed: make(map[string]struct{}),
	}
}

func (tx *notifyingStoreTx) Update(id string, opts UpdateOpts) error {
	if err := tx.backing.Update(id, opts); err != nil {
		return err
	}
	tx.touch(id)
	return nil
}

func (tx *notifyingStoreTx) SetMetadataBatch(id string, kvs map[string]string) error {
	if err := tx.backing.SetMetadataBatch(id, kvs); err != nil {
		return err
	}
	tx.touch(id)
	return nil
}

func (tx *notifyingStoreTx) Close(id string) error {
	if err := tx.backing.Close(id); err != nil {
		return err
	}
	tx.touch(id)
	tx.closed[id] = struct{}{}
	return nil
}

func (tx *notifyingStoreTx) touch(id string) {
	if id == "" {
		return
	}
	if _, ok := tx.seen[id]; ok {
		return
	}
	tx.seen[id] = struct{}{}
	tx.ids = append(tx.ids, id)
}
