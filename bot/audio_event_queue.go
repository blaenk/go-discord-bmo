package bot

// AudioEventQueue represents a blocking audio event queue.
import "sync"

type AudioEventQueue struct {
	cond  *sync.Cond
	queue []*AudioEvent
}

func NewAudioEventQueue() *AudioEventQueue {
	return &AudioEventQueue{
		queue: make([]*AudioEvent, 0, 10),
		cond:  sync.NewCond(new(sync.Mutex)),
	}
}

func (q *AudioEventQueue) Clear() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	for _, event := range q.queue {
		event.audio.Close()
	}

	q.queue = make([]*AudioEvent, 0, 10)
}

func (q *AudioEventQueue) Enqueue(event *AudioEvent) {
	q.cond.L.Lock()

	q.queue = append(q.queue, event)

	q.cond.Signal()
	q.cond.L.Unlock()
}

// Preempt swaps the head and the event after it.
func (q *AudioEventQueue) Preempt() {
	q.cond.L.Lock()

	if len(q.queue) < 2 {
		return
	}

	head, next := q.queue[0], q.queue[1]

	q.queue[0] = next
	q.queue[1] = head

	q.cond.L.Unlock()
}

func (q *AudioEventQueue) EnqueueFront(event *AudioEvent) {
	q.cond.L.Lock()

	q.queue = append([]*AudioEvent{event}, q.queue...)

	q.cond.L.Unlock()
}

func (q *AudioEventQueue) Dequeue() *AudioEvent {
	q.cond.L.Lock()

	for len(q.queue) == 0 {
		q.cond.Wait()
	}

	var event *AudioEvent
	event, q.queue = q.queue[0], q.queue[1:]

	q.cond.Signal()
	q.cond.L.Unlock()

	return event
}
