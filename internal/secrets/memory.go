package secrets

import "sync"

// Memory es un Store en memoria para tests y para contraseñas "pedir cada vez" que no deben persistir.
type Memory struct {
	mu sync.Mutex
	m  map[string]Secret
}

func NewMemory() *Memory { return &Memory{m: map[string]Secret{}} }

func (s *Memory) Get(profileID string, kind Kind) (Secret, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[account(profileID, kind)]
	return v, ok, nil
}

func (s *Memory) Set(profileID string, kind Kind, value Secret) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[account(profileID, kind)] = value
	return nil
}

func (s *Memory) Delete(profileID string, kind Kind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, account(profileID, kind))
	return nil
}

func (s *Memory) DeleteAll(profileID string) error {
	for _, k := range allKinds {
		_ = s.Delete(profileID, k)
	}
	return nil
}
