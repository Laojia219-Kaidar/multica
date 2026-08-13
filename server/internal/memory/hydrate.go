package memory

import "context"

// Hydrate reconstructs the store's in-memory state for one employee from the
// persistence repository: candidates plus their append-only promotion and
// revocation records. It is the resume-after-restart read-back path.
func (s *Store) Hydrate(ctx context.Context, repo *Repository, employeeID string) error {
	cands, err := repo.ListByEmployee(ctx, employeeID)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range cands {
		c := cands[i]
		s.candidates[c.ID] = &c
	}
	for _, c := range cands {
		promos, err := repo.ListPromotions(ctx, c.ID)
		if err != nil {
			return err
		}
		s.promotions = append(s.promotions, promos...)
		revs, err := repo.ListRevocations(ctx, c.ID)
		if err != nil {
			return err
		}
		s.revocations = append(s.revocations, revs...)
	}
	return nil
}
