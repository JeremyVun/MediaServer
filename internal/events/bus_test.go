package events

import (
	"testing"
	"time"
)

func TestPublishReachesAllSubscribers(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	ch1, cancel1 := bus.Subscribe()
	ch2, cancel2 := bus.Subscribe()
	defer cancel1()
	defer cancel2()

	bus.Publish(Event{Type: ItemAdded, Payload: 42})

	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case e := <-ch:
			if e.Type != ItemAdded || e.Payload != 42 {
				t.Errorf("subscriber %d got %+v", i, e)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d never received the event", i)
		}
	}
}

func TestSlowSubscriberNeverBlocksPublish(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	ch, cancel := bus.Subscribe() // never drained until the end
	defer cancel()

	done := make(chan struct{})
	go func() {
		for i := range subscriberBuffer * 3 {
			bus.Publish(Event{Type: JobProgress, Payload: i})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on a slow subscriber")
	}

	// The buffer holds the newest events; the oldest were dropped.
	var got []int
	for {
		select {
		case e := <-ch:
			got = append(got, e.Payload.(int))
			continue
		default:
		}
		break
	}
	if len(got) == 0 || len(got) > subscriberBuffer {
		t.Fatalf("drained %d events, want 1..%d", len(got), subscriberBuffer)
	}
	last := got[len(got)-1]
	if last != subscriberBuffer*3-1 {
		t.Errorf("newest event = %d, want %d (drop-oldest, keep-newest)", last, subscriberBuffer*3-1)
	}
}

func TestCancelClosesChannelAndIsIdempotent(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	ch, cancel := bus.Subscribe()
	cancel()
	cancel() // must not panic

	if _, open := <-ch; open {
		t.Error("channel still open after cancel")
	}

	// Publishing after cancel must not panic or deliver.
	bus.Publish(Event{Type: RootStatus})
}

func TestCloseShutsDownSubscribers(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe()
	bus.Close()
	defer cancel()

	if _, open := <-ch; open {
		t.Error("channel still open after bus close")
	}

	// Subscribe after close yields a closed channel, not a hang.
	ch2, _ := bus.Subscribe()
	if _, open := <-ch2; open {
		t.Error("subscribe after close returned a live channel")
	}
}
