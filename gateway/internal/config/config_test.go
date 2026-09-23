package config

import "testing"

func TestDeletedRouteStaysDeleted(t *testing.T) {
	t.Setenv("RLCD_GATEWAY_HOME", t.TempDir())
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRoute("openrouter"); err != nil {
		t.Fatal(err)
	}
	s2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Get().Routes["openrouter"]; ok {
		t.Error("deleted route came back from the defaults after a reload")
	}
	if _, ok := s2.Get().Routes["claude-sub"]; !ok {
		t.Error("remaining route lost")
	}
}
