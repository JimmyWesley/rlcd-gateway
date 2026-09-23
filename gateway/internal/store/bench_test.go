package store

import (
	"fmt"
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
)

// BenchmarkSaveTurn saves turn 50 of a growing session (~250 KB body) over
// a store that already holds the earlier turns: the steady state of an agent.
func BenchmarkSaveTurn(b *testing.B) {
	dir := b.TempDir()
	s, _ := Open(dir)
	body := growingTurn(50)
	x, _ := ir.ParseFor("", []byte(body))
	_ = s.Save(&Detail{Record: Record{ID: "20260923T000000-warm"}, XRay: x, RequestBody: body})
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Save(&Detail{Record: Record{ID: fmt.Sprintf("20260923T000001-%d", i)}, XRay: x, RequestBody: body, ResponseBody: "ok"}); err != nil {
			b.Fatal(err)
		}
	}
}
