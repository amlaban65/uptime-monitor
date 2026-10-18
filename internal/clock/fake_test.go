package clock

import (
	"testing"
	"time"
)

func TestFakeAfterFiresOnAdvance(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := NewFake(start)
	ch := f.After(10 * time.Second)

	f.Advance(9 * time.Second)
	select {
	case <-ch:
		t.Fatal("fired before deadline")
	default:
	}

	f.Advance(time.Second)
	select {
	case got := <-ch:
		if want := start.Add(10 * time.Second); !got.Equal(want) {
			t.Fatalf("fired with %v, want %v", got, want)
		}
	default:
		t.Fatal("did not fire at deadline")
	}
}

func TestFakeBlockUntil(t *testing.T) {
	f := NewFake(time.Unix(0, 0))
	done := make(chan struct{})
	go func() {
		<-f.After(time.Second)
		close(done)
	}()
	f.BlockUntil(1)
	f.Advance(time.Second)
	<-done
}
